// Package smtpsend delivers composed messages through Gmail's SMTP server.
package smtpsend

import (
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// Config describes an SMTP submission server.
type Config struct {
	Host      string        // default smtp.gmail.com
	Port      int           // default 465 (implicit TLS); 587 uses STARTTLS
	Username  string        // full email address
	Password  string        // Google app password
	Timeout   time.Duration // default 60s
	TLSConfig *tls.Config   // optional override (tests)
	Debug     io.Writer     // optional protocol trace, credentials redacted
}

const (
	DefaultHost    = "smtp.gmail.com"
	DefaultPort    = 465
	DefaultTimeout = 60 * time.Second
)

// AppPasswordsURL is where Google app passwords are created.
const AppPasswordsURL = "https://myaccount.google.com/apppasswords"

// AuthError reports that the server rejected the credentials.
type AuthError struct {
	User   string
	Code   int    // SMTP reply code, e.g. 535
	Server string // the server's reply text, on one line
}

func (e *AuthError) Error() string {
	msg := fmt.Sprintf("SMTP authentication failed for %s: check the app password (%s) and that 2-Step Verification is enabled", e.User, AppPasswordsURL)
	if e.Server != "" {
		msg += fmt.Sprintf(" (server said: %d %s)", e.Code, e.Server)
	}
	return msg
}

// RecipientError reports that the server refused one envelope recipient.
type RecipientError struct {
	Recipient string
	Err       error
}

func (e *RecipientError) Error() string {
	return fmt.Sprintf("SMTP server rejected recipient %s: %v", e.Recipient, e.Err)
}

func (e *RecipientError) Unwrap() error { return e.Err }

// implicitTLS reports whether a port speaks TLS from the first byte; tests
// replace it because they can't listen on 465.
var implicitTLS = func(port int) bool { return port == 465 }

// Send delivers raw to rcpts with from as the envelope sender.
func Send(cfg Config, from string, rcpts []string, raw []byte) error {
	if len(rcpts) == 0 {
		return errors.New("smtp: no recipients")
	}
	if err := checkAddr(from); err != nil {
		return fmt.Errorf("smtp: sender: %w", err)
	}
	for _, r := range rcpts {
		if err := checkAddr(r); err != nil {
			return fmt.Errorf("smtp: recipient: %w", err)
		}
	}
	if len(raw) == 0 {
		return errors.New("smtp: empty message")
	}

	c, err := dial(cfg)
	if err != nil {
		return err
	}
	defer c.close()
	if err := c.auth(); err != nil {
		return err
	}
	if err := c.send(from, rcpts, raw); err != nil {
		return err
	}
	c.quit()
	return nil
}

// Check connects and authenticates without sending anything.
func Check(cfg Config) error {
	c, err := dial(cfg)
	if err != nil {
		return err
	}
	defer c.close()
	if err := c.auth(); err != nil {
		return err
	}
	c.quit()
	return nil
}

func checkAddr(a string) error {
	if strings.TrimSpace(a) == "" {
		return errors.New("empty address")
	}
	if strings.ContainsAny(a, "<>\r\n\t ") {
		return fmt.Errorf("invalid address %q", a)
	}
	return nil
}

// client is a minimal SMTP submission client. It's written against
// net/textproto rather than net/smtp so the trace can follow the session
// across STARTTLS and redact credentials at the command level.
type client struct {
	cfg     Config
	addr    string
	conn    net.Conn
	text    *textproto.Conn
	ext     map[string]string // EHLO keywords (upper case) to parameters
	tlsDone bool
}

func withDefaults(cfg Config) Config {
	if cfg.Host == "" {
		cfg.Host = DefaultHost
	}
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	return cfg
}

func (c *client) tlsConfig() *tls.Config {
	var tc *tls.Config
	if c.cfg.TLSConfig != nil {
		tc = c.cfg.TLSConfig.Clone()
	} else {
		tc = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if tc.ServerName == "" {
		tc.ServerName = c.cfg.Host
	}
	return tc
}

// dial connects, negotiates TLS (implicit or STARTTLS) and says EHLO.
func dial(cfg Config) (*client, error) {
	cfg = withDefaults(cfg)
	c := &client{cfg: cfg, addr: net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))}
	implicit := implicitTLS(cfg.Port)

	mode := "STARTTLS"
	if implicit {
		mode = "implicit TLS"
	}
	c.tracef("* connecting to %s (%s)", c.addr, mode)
	raw, err := (&net.Dialer{Timeout: cfg.Timeout}).Dial("tcp", c.addr)
	if err != nil {
		return nil, fmt.Errorf("smtp: connecting to %s: %w", c.addr, err)
	}
	// Every read and write gets a fresh deadline, so a stalled server fails
	// after Timeout without capping how long a large upload may take.
	conn := net.Conn(&deadlineConn{Conn: raw, timeout: cfg.Timeout})
	if implicit {
		tc := tls.Client(conn, c.tlsConfig())
		if err := tc.Handshake(); err != nil {
			conn.Close()
			return nil, fmt.Errorf("smtp: TLS handshake with %s: %w", c.addr, err)
		}
		c.traceTLS(tc)
		conn = tc
		c.tlsDone = true
	}
	c.conn = conn
	c.text = textproto.NewConn(conn)

	if _, _, err := c.read(220); err != nil {
		c.close()
		return nil, fmt.Errorf("smtp: %s greeting: %w", c.addr, err)
	}
	if err := c.ehlo(); err != nil {
		c.close()
		return nil, err
	}
	if !implicit {
		if err := c.startTLS(); err != nil {
			c.close()
			return nil, err
		}
	}
	return c, nil
}

func (c *client) ehlo() error {
	_, msg, err := c.cmd(250, "EHLO localhost")
	if err != nil {
		return fmt.Errorf("smtp: EHLO: %w", err)
	}
	c.ext = map[string]string{}
	lines := strings.Split(msg, "\n")
	for _, l := range lines[1:] { // the first line is the server's greeting
		kw, params, _ := strings.Cut(strings.TrimSpace(l), " ")
		if kw != "" {
			c.ext[strings.ToUpper(kw)] = params
		}
	}
	return nil
}

func (c *client) startTLS() error {
	if _, ok := c.ext["STARTTLS"]; !ok {
		return fmt.Errorf("smtp: %s does not offer STARTTLS; refusing to send credentials without TLS", c.addr)
	}
	if _, _, err := c.cmd(220, "STARTTLS"); err != nil {
		return fmt.Errorf("smtp: STARTTLS: %w", err)
	}
	tc := tls.Client(c.conn, c.tlsConfig())
	if err := tc.Handshake(); err != nil {
		return fmt.Errorf("smtp: TLS handshake with %s: %w", c.addr, err)
	}
	c.traceTLS(tc)
	c.conn = tc
	c.text = textproto.NewConn(tc)
	c.tlsDone = true
	return c.ehlo() // capabilities may change once encrypted
}

func (c *client) auth() error {
	if !c.tlsDone {
		return errors.New("smtp: refusing to authenticate without TLS")
	}
	if c.cfg.Username == "" || c.cfg.Password == "" {
		return errors.New("smtp: a username and app password are required")
	}
	mechs, ok := c.ext["AUTH"]
	if !ok || !hasWordFold(mechs, "PLAIN") {
		return fmt.Errorf("smtp: %s does not support AUTH PLAIN (offers %q)", c.addr, mechs)
	}
	resp := base64.StdEncoding.EncodeToString([]byte("\x00" + c.cfg.Username + "\x00" + c.cfg.Password))
	code, msg, err := c.cmdRedacted("AUTH PLAIN <redacted>", "AUTH PLAIN "+resp)
	if err == nil && code == 334 {
		// The server ignored the initial response and asked for it again.
		code, msg, err = c.cmdRedacted("<redacted>", resp)
	}
	if err != nil {
		return fmt.Errorf("smtp: AUTH: %w", err)
	}
	switch {
	case code == 235:
		return nil
	case code == 534 || code == 535:
		return &AuthError{User: c.cfg.Username, Code: code, Server: oneLine(msg)}
	default:
		return fmt.Errorf("smtp: AUTH: %w", &textproto.Error{Code: code, Msg: msg})
	}
}

func (c *client) send(from string, rcpts []string, raw []byte) error {
	mail := "MAIL FROM:<" + from + ">"
	if _, ok := c.ext["SIZE"]; ok {
		mail += " SIZE=" + strconv.Itoa(len(raw))
	}
	if _, ok := c.ext["8BITMIME"]; ok && !is7bit(raw) {
		mail += " BODY=8BITMIME"
	}
	if _, ok := c.ext["SMTPUTF8"]; ok && !isASCII(from+strings.Join(rcpts, "")) {
		mail += " SMTPUTF8"
	}
	if _, _, err := c.cmd(250, "%s", mail); err != nil {
		return fmt.Errorf("smtp: MAIL FROM %s: %w", from, err)
	}
	for _, r := range rcpts {
		if _, _, err := c.cmd(25, "RCPT TO:<%s>", r); err != nil {
			return &RecipientError{Recipient: r, Err: err}
		}
	}
	if _, _, err := c.cmd(354, "DATA"); err != nil {
		return fmt.Errorf("smtp: DATA: %w", err)
	}
	dw := c.text.DotWriter()
	if _, err := dw.Write(raw); err != nil {
		dw.Close()
		return fmt.Errorf("smtp: sending message data: %w", err)
	}
	if err := dw.Close(); err != nil {
		return fmt.Errorf("smtp: sending message data: %w", err)
	}
	c.tracef("C: <%d bytes of message data>", len(raw))
	c.tracef("C: .")
	if _, _, err := c.read(250); err != nil {
		return fmt.Errorf("smtp: message not accepted: %w", err)
	}
	return nil
}

// quit ends the session politely; the message is already accepted, so
// errors are ignored.
func (c *client) quit() {
	c.cmd(221, "QUIT")
}

func (c *client) close() {
	if c.conn != nil {
		c.conn.Close()
	}
}

// cmd sends a command and reads the reply, which must match expect (a full
// code, or a one- or two-digit prefix as in textproto.Reader.ReadResponse).
func (c *client) cmd(expect int, format string, args ...any) (int, string, error) {
	line := fmt.Sprintf(format, args...)
	c.tracef("C: %s", line)
	if err := c.text.PrintfLine("%s", line); err != nil {
		return 0, "", err
	}
	return c.read(expect)
}

// cmdRedacted sends a command whose trace line is replaced by shown, and
// returns whatever reply comes back for the caller to judge.
func (c *client) cmdRedacted(shown, line string) (int, string, error) {
	c.tracef("C: %s", shown)
	if err := c.text.PrintfLine("%s", line); err != nil {
		return 0, "", err
	}
	code, msg, err := c.text.ReadResponse(0)
	c.traceReply(code, msg)
	return code, msg, err
}

func (c *client) read(expect int) (int, string, error) {
	code, msg, err := c.text.ReadResponse(expect)
	c.traceReply(code, msg)
	return code, msg, err
}

func (c *client) tracef(format string, args ...any) {
	if c.cfg.Debug != nil {
		fmt.Fprintf(c.cfg.Debug, format+"\n", args...)
	}
}

func (c *client) traceReply(code int, msg string) {
	if c.cfg.Debug == nil || code == 0 {
		return
	}
	lines := strings.Split(msg, "\n")
	for i, l := range lines {
		sep := "-"
		if i == len(lines)-1 {
			sep = " "
		}
		c.tracef("S: %d%s%s", code, sep, l)
	}
}

func (c *client) traceTLS(tc *tls.Conn) {
	if c.cfg.Debug == nil {
		return
	}
	st := tc.ConnectionState()
	c.tracef("* TLS established (%s, %s)", tls.VersionName(st.Version), tls.CipherSuiteName(st.CipherSuite))
}

// deadlineConn refreshes the connection deadline before every read and
// write.
type deadlineConn struct {
	net.Conn
	timeout time.Duration
}

func (d *deadlineConn) Read(p []byte) (int, error) {
	d.Conn.SetReadDeadline(time.Now().Add(d.timeout))
	return d.Conn.Read(p)
}

func (d *deadlineConn) Write(p []byte) (int, error) {
	d.Conn.SetWriteDeadline(time.Now().Add(d.timeout))
	return d.Conn.Write(p)
}

func hasWordFold(s, word string) bool {
	for _, w := range strings.Fields(s) {
		if strings.EqualFold(w, word) {
			return true
		}
	}
	return false
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func is7bit(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 {
			return false
		}
	}
	return true
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
