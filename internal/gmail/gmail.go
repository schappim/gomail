// Package gmail is an IMAP client for Gmail that uses Gmail's IMAP extensions
// (X-GM-RAW search, X-GM-MSGID / X-GM-THRID ids, X-GM-LABELS) on top of
// github.com/emersion/go-imap v1.
package gmail

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap/commands"
	"github.com/emersion/go-message/charset"

	"gomail/internal/model"
)

// ErrNotFound is returned when a message or thread id does not exist.
var ErrNotFound = errors.New("not found")

// ErrNotDraft is returned by DeleteDraft when the message is not in the
// drafts mailbox.
var ErrNotDraft = errors.New("not a draft")

// ErrAuth is matched (via errors.Is) by the error Dial returns when Gmail
// rejects the username or app password.
var ErrAuth = errors.New("authentication failed")

// Config describes how to connect.
type Config struct {
	Host     string        // default imap.gmail.com
	Port     int           // default 993 (implicit TLS)
	Username string        // full email address
	Password string        // Google app password
	Timeout  time.Duration // default 60s
	Debug    io.Writer     // optional IMAP trace with credentials redacted
}

const (
	defaultHost    = "imap.gmail.com"
	defaultPort    = 993
	defaultTimeout = 60 * time.Second
)

func (cfg Config) withDefaults() Config {
	if cfg.Host == "" {
		cfg.Host = defaultHost
	}
	if cfg.Port == 0 {
		cfg.Port = defaultPort
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	return cfg
}

func (cfg Config) addr() string {
	return net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
}

// Client is a logged-in Gmail IMAP session. It is not safe for concurrent use.
// Config.Timeout bounds each command and also how long the session may sit
// idle between commands before the connection is dropped.
type Client struct {
	imap      *client.Client
	user      string
	mailboxes []*mailbox          // LIST cache; nil until first needed
	sleep     func(time.Duration) // replaced in tests
}

// SearchOptions controls Search.
type SearchOptions struct {
	Mailbox  string // label name or alias (inbox, sent, drafts, all, trash, spam, starred, important); default "all"
	Query    string // Gmail search syntax (X-GM-RAW); empty means everything in Mailbox
	Limit    int    // default 25
	Offset   int    // skip this many of the newest matches
	Snippets bool   // fill MessageSummary.Snippet
}

// SearchResult is a page of results, newest first.
type SearchResult struct {
	Mailbox  string
	Messages []model.MessageSummary
	Total    int
}

// RawMessage pairs a summary with its RFC 822 bytes.
type RawMessage struct {
	Summary model.MessageSummary
	Raw     []byte
}

func init() {
	// Lets go-imap decode RFC 2047 encoded-words in envelopes and body
	// structure parameters for every charset, not just UTF-8 and ASCII.
	if imap.CharsetReader == nil {
		imap.CharsetReader = charset.Reader
	}
}

// Dial connects to Gmail over implicit TLS and logs in with the app password.
func Dial(cfg Config) (*Client, error) {
	cfg = cfg.withDefaults()
	return dial(cfg, func() (net.Conn, error) {
		dialer := &net.Dialer{Timeout: cfg.Timeout}
		return tls.DialWithDialer(dialer, "tcp", cfg.addr(), &tls.Config{ServerName: cfg.Host})
	})
}

// dial logs in over a connection obtained from connect. Tests use it to talk
// plaintext to a fake server.
func dial(cfg Config, connect func() (net.Conn, error)) (*Client, error) {
	cfg = cfg.withDefaults()
	conn, err := connect()
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", cfg.addr(), err)
	}

	password := normalizeAppPassword(cfg.Password)
	var trace *traceConn
	if cfg.Debug != nil {
		trace = newTraceConn(conn, cfg.Debug, cfg.Password, password)
		conn = trace
	}

	// client.New blocks reading the greeting; bound that wait. Afterwards
	// client.Timeout sets a fresh deadline for every command.
	if err := conn.SetDeadline(time.Now().Add(cfg.Timeout)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("connect to %s: %w", cfg.addr(), err)
	}
	ic, err := client.New(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("IMAP greeting from %s: %w", cfg.addr(), err)
	}
	ic.Timeout = cfg.Timeout
	ic.ErrorLog = log.New(io.Discard, "", 0)
	if trace != nil {
		ic.ErrorLog = log.New(logWriter{trace}, "go-imap: ", 0)
	}

	if err := login(ic, trace, cfg.Username, password); err != nil {
		ic.Terminate()
		return nil, err
	}
	return &Client{imap: ic, user: cfg.Username, sleep: time.Sleep}, nil
}

// login runs LOGIN itself (rather than client.Login) so a NO reply can be told
// apart from a network failure.
func login(ic *client.Client, trace *traceConn, user, password string) error {
	if trace != nil {
		trace.redactLogin(true)
		defer trace.redactLogin(false)
	}
	status, err := ic.Execute(&commands.Login{Username: user, Password: password}, nil)
	if err != nil {
		return fmt.Errorf("IMAP login for %s: %w", user, err)
	}
	if status.Type != imap.StatusRespOk {
		return &authError{user: user, server: statusText(status)}
	}
	ic.SetState(imap.AuthenticatedState, nil)
	// Gmail advertises more capabilities (MOVE, UIDPLUS, LIST-STATUS) once
	// authenticated, so refresh the cached set.
	if _, err := ic.Capability(); err != nil {
		return fmt.Errorf("IMAP CAPABILITY after login: %w", err)
	}
	return nil
}

type authError struct {
	user, server string
}

func (e *authError) Error() string {
	return fmt.Sprintf("IMAP login failed for %s: check the app password (https://myaccount.google.com/apppasswords), "+
		"that 2-Step Verification is on and IMAP is enabled (server said: %s)", e.user, e.server)
}

func (e *authError) Is(target error) bool { return target == ErrAuth }

// normalizeAppPassword strips the spaces Google shows inside app passwords
// ("abcd efgh ijkl mnop"). Anything that isn't shaped like an app password is
// returned unchanged.
func normalizeAppPassword(pw string) string {
	compact := strings.ReplaceAll(pw, " ", "")
	if len(compact) != 16 || compact == pw {
		return pw
	}
	for _, r := range compact {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return pw
		}
	}
	return compact
}

// Close logs out and closes the connection.
func (c *Client) Close() error {
	if c == nil || c.imap == nil {
		return nil
	}
	select {
	case <-c.imap.LoggedOut():
		return nil // connection already gone (server BYE, idle timeout)
	default:
	}
	err := c.imap.Logout()
	c.imap.Terminate()
	if err != nil && !errors.Is(err, client.ErrAlreadyLoggedOut) {
		return fmt.Errorf("IMAP logout: %w", err)
	}
	return nil
}
