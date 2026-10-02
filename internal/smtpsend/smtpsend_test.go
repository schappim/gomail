package smtpsend

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"io"
	"math/big"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// testCert returns a self-signed certificate for 127.0.0.1 and a pool that
// trusts it.
func testCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fake smtp"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}, pool
}

// fakeServer is a just-enough SMTP submission server.
type fakeServer struct {
	implicit   bool // TLS from the first byte
	starttls   bool // advertise STARTTLS on the plain connection
	user, pass string
	rejectRcpt string
	silent     bool // never send a greeting

	tlsConfig *tls.Config
	ln        net.Listener
	port      int
	wg        sync.WaitGroup // sessions in progress

	mu          sync.Mutex
	authUser    string
	authPass    string
	authOverTLS bool
	mailFrom    string
	rcpts       []string
	data        []byte
	commands    []string
	quit        bool
}

func startServer(t *testing.T, s *fakeServer) (*fakeServer, *x509.CertPool) {
	t.Helper()
	cert, pool := testCert(t)
	s.tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.ln = ln
	s.port = ln.Addr().(*net.TCPAddr).Port
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.serve(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		<-acceptDone
		s.wg.Wait()
	})
	if s.implicit {
		old := implicitTLS
		implicitTLS = func(port int) bool { return port == s.port }
		t.Cleanup(func() { implicitTLS = old })
	}
	return s, pool
}

func (s *fakeServer) serve(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if s.silent {
		io.Copy(io.Discard, conn)
		return
	}
	tlsOn := false
	if s.implicit {
		conn = tls.Server(conn, s.tlsConfig)
		tlsOn = true
	}
	tp := textproto.NewConn(conn)
	tp.PrintfLine("220 fake.example ESMTP ready")
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		s.mu.Lock()
		s.commands = append(s.commands, strings.ToUpper(verb))
		s.mu.Unlock()
		switch strings.ToUpper(verb) {
		case "EHLO":
			exts := []string{"fake.example at your service", "SIZE 35882577", "8BITMIME"}
			if !tlsOn && s.starttls {
				exts = append(exts, "STARTTLS")
			}
			exts = append(exts, "AUTH LOGIN PLAIN XOAUTH2", "ENHANCEDSTATUSCODES")
			for i, e := range exts {
				sep := "-"
				if i == len(exts)-1 {
					sep = " "
				}
				tp.PrintfLine("250%s%s", sep, e)
			}
		case "STARTTLS":
			tp.PrintfLine("220 2.0.0 Ready to start TLS")
			tc := tls.Server(conn, s.tlsConfig)
			if err := tc.Handshake(); err != nil {
				return
			}
			conn, tp, tlsOn = tc, textproto.NewConn(tc), true
		case "AUTH":
			mech, resp, _ := strings.Cut(arg, " ")
			dec, _ := base64.StdEncoding.DecodeString(resp)
			parts := strings.Split(string(dec), "\x00")
			s.mu.Lock()
			if strings.EqualFold(mech, "PLAIN") && len(parts) == 3 {
				s.authUser, s.authPass, s.authOverTLS = parts[1], parts[2], tlsOn
			}
			ok := s.authUser == s.user && s.authPass == s.pass
			s.mu.Unlock()
			if ok {
				tp.PrintfLine("235 2.7.0 Accepted")
			} else {
				tp.PrintfLine("535-5.7.8 Username and Password not accepted. For more information, go to")
				tp.PrintfLine("535 5.7.8  https://support.google.com/mail/?p=BadCredentials x1-gsmtp")
			}
		case "MAIL":
			s.mu.Lock()
			s.mailFrom = arg
			s.mu.Unlock()
			tp.PrintfLine("250 2.1.0 OK")
		case "RCPT":
			addr := strings.TrimSuffix(strings.TrimPrefix(arg, "TO:<"), ">")
			if addr == s.rejectRcpt {
				tp.PrintfLine("550 5.1.1 The email account that you tried to reach does not exist.")
				continue
			}
			s.mu.Lock()
			s.rcpts = append(s.rcpts, addr)
			s.mu.Unlock()
			tp.PrintfLine("250 2.1.5 OK")
		case "DATA":
			tp.PrintfLine("354 Go ahead")
			data, err := io.ReadAll(tp.DotReader())
			if err != nil {
				return
			}
			s.mu.Lock()
			s.data = data
			s.mu.Unlock()
			tp.PrintfLine("250 2.0.0 OK 1700000000 x1-gsmtp")
		case "QUIT":
			s.mu.Lock()
			s.quit = true
			s.mu.Unlock()
			tp.PrintfLine("221 2.0.0 closing connection")
			return
		default:
			tp.PrintfLine("502 5.5.1 Unrecognized command.")
		}
	}
}

func (s *fakeServer) config(pool *x509.CertPool, password string) Config {
	return Config{
		Host:      "127.0.0.1",
		Port:      s.port,
		Username:  s.user,
		Password:  password,
		Timeout:   5 * time.Second,
		TLSConfig: &tls.Config{RootCAs: pool},
	}
}

const testMessage = "From: alice@example.com\r\nTo: bob@example.com\r\nSubject: hi\r\n\r\nHello\r\n.leading dot line\r\n.\r\nbye\r\n"

func TestSendImplicitTLS(t *testing.T) {
	s, pool := startServer(t, &fakeServer{implicit: true, user: "alice@example.com", pass: "abcd efgh ijkl mnop"})
	var trace bytes.Buffer
	cfg := s.config(pool, s.pass)
	cfg.Debug = &trace
	rcpts := []string{"bob@example.com", "carol@example.com", "dave@example.org"}
	if err := Send(cfg, "alice@example.com", rcpts, []byte(testMessage)); err != nil {
		t.Fatalf("Send: %v\ntrace:\n%s", err, trace.String())
	}
	s.wg.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authUser != "alice@example.com" || s.authPass != "abcd efgh ijkl mnop" || !s.authOverTLS {
		t.Errorf("AUTH PLAIN got %q / %q (TLS %v)", s.authUser, s.authPass, s.authOverTLS)
	}
	if want := "FROM:<alice@example.com> SIZE=" + strconv.Itoa(len(testMessage)); s.mailFrom != want {
		t.Errorf("MAIL %q, want %q", s.mailFrom, want)
	}
	if strings.Join(s.rcpts, ",") != strings.Join(rcpts, ",") {
		t.Errorf("RCPT TO = %v, want %v", s.rcpts, rcpts)
	}
	// The dot reader undoes dot-stuffing and turns CRLF into LF.
	if want := strings.ReplaceAll(testMessage, "\r\n", "\n"); string(s.data) != want {
		t.Errorf("DATA = %q, want %q", s.data, want)
	}
	if !s.quit {
		t.Errorf("no QUIT")
	}

	tr := trace.String()
	if strings.Contains(tr, s.pass) || strings.Contains(tr, base64.StdEncoding.EncodeToString([]byte("\x00alice@example.com\x00"+s.pass))) {
		t.Errorf("trace leaks the password:\n%s", tr)
	}
	for _, want := range []string{"C: AUTH PLAIN <redacted>", "S: 235 2.7.0 Accepted", "C: RCPT TO:<carol@example.com>", "S: 250-SIZE 35882577", "* TLS established", "bytes of message data>"} {
		if !strings.Contains(tr, want) {
			t.Errorf("trace missing %q:\n%s", want, tr)
		}
	}
	if strings.Contains(tr, "leading dot line") {
		t.Errorf("trace includes the message body")
	}
}

func TestSendSTARTTLS(t *testing.T) {
	s, pool := startServer(t, &fakeServer{starttls: true, user: "alice@example.com", pass: "secret"})
	if err := Send(s.config(pool, "secret"), "alice@example.com", []string{"bob@example.com"}, []byte(testMessage)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.authOverTLS {
		t.Errorf("authenticated before STARTTLS")
	}
	if got := strings.Join(s.commands, " "); got != "EHLO STARTTLS EHLO AUTH MAIL RCPT DATA QUIT" {
		t.Errorf("commands = %s", got)
	}
	if string(s.data) != strings.ReplaceAll(testMessage, "\r\n", "\n") {
		t.Errorf("DATA = %q", s.data)
	}
}

func TestSTARTTLSRequired(t *testing.T) {
	s, pool := startServer(t, &fakeServer{starttls: false, user: "alice@example.com", pass: "secret"})
	err := Send(s.config(pool, "secret"), "alice@example.com", []string{"bob@example.com"}, []byte(testMessage))
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("err = %v, want a STARTTLS error", err)
	}
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authUser != "" {
		t.Errorf("credentials sent over plaintext")
	}
}

func TestAuthFailure(t *testing.T) {
	s, pool := startServer(t, &fakeServer{implicit: true, user: "alice@example.com", pass: "right"})
	err := Send(s.config(pool, "wrong"), "alice@example.com", []string{"bob@example.com"}, []byte(testMessage))
	var ae *AuthError
	if !errors.As(err, &ae) || ae.Code != 535 {
		t.Fatalf("err = %v, want *AuthError 535", err)
	}
	want := "SMTP authentication failed for alice@example.com: check the app password (https://myaccount.google.com/apppasswords) and that 2-Step Verification is enabled"
	if !strings.HasPrefix(err.Error(), want) {
		t.Errorf("err = %q\nwant prefix %q", err, want)
	}
	if strings.Contains(err.Error(), "\n") || !strings.Contains(err.Error(), "BadCredentials") {
		t.Errorf("server detail should be kept on one line: %q", err)
	}
	if err := Check(s.config(pool, "wrong")); !errors.As(err, &ae) {
		t.Errorf("Check err = %v", err)
	}
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.commands {
		if c == "MAIL" {
			t.Errorf("sent MAIL after failed AUTH")
		}
	}
}

func TestRecipientRejected(t *testing.T) {
	s, pool := startServer(t, &fakeServer{implicit: true, user: "u@example.com", pass: "p", rejectRcpt: "nobody@example.com"})
	err := Send(s.config(pool, "p"), "u@example.com", []string{"bob@example.com", "nobody@example.com"}, []byte(testMessage))
	var re *RecipientError
	if !errors.As(err, &re) || re.Recipient != "nobody@example.com" {
		t.Fatalf("err = %v, want RecipientError for nobody@example.com", err)
	}
	if !strings.Contains(err.Error(), "nobody@example.com") || !strings.Contains(err.Error(), "550") {
		t.Errorf("err = %q", err)
	}
	var tpErr *textproto.Error
	if !errors.As(err, &tpErr) || tpErr.Code != 550 {
		t.Errorf("underlying reply not unwrappable: %v", err)
	}
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data != nil {
		t.Errorf("DATA sent despite a rejected recipient")
	}
}

func TestCheck(t *testing.T) {
	s, pool := startServer(t, &fakeServer{implicit: true, user: "u@example.com", pass: "p"})
	if err := Check(s.config(pool, "p")); err != nil {
		t.Fatalf("Check: %v", err)
	}
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if got := strings.Join(s.commands, " "); got != "EHLO AUTH QUIT" {
		t.Errorf("commands = %s", got)
	}
}

func TestSendValidation(t *testing.T) {
	cfg := Config{Host: "127.0.0.1", Port: 1, Username: "u", Password: "p"}
	if err := Send(cfg, "a@b.com", nil, []byte("x")); err == nil || !strings.Contains(err.Error(), "no recipients") {
		t.Errorf("no recipients: %v", err)
	}
	if err := Send(cfg, "a@b.com", []string{"x@y.com>\r\nRCPT TO:<evil@z.com"}, []byte("x")); err == nil {
		t.Errorf("recipient with CRLF accepted")
	}
	if err := Send(cfg, "", []string{"x@y.com"}, []byte("x")); err == nil {
		t.Errorf("empty sender accepted")
	}
}

func TestTimeout(t *testing.T) {
	s, pool := startServer(t, &fakeServer{silent: true})
	cfg := s.config(pool, "p")
	cfg.Timeout = 200 * time.Millisecond
	start := time.Now()
	err := Check(cfg)
	if err == nil {
		t.Fatal("Check succeeded against a silent server")
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Errorf("err = %v, want a timeout", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("timeout took %v", time.Since(start))
	}
}

func TestDefaults(t *testing.T) {
	c := withDefaults(Config{})
	if c.Host != "smtp.gmail.com" || c.Port != 465 || c.Timeout != 60*time.Second {
		t.Errorf("defaults = %+v", c)
	}
	if !implicitTLS(465) || implicitTLS(587) {
		t.Errorf("implicitTLS wrong")
	}
	cl := &client{cfg: withDefaults(Config{TLSConfig: &tls.Config{}})}
	if cl.tlsConfig().ServerName != "smtp.gmail.com" {
		t.Errorf("ServerName not defaulted")
	}
}
