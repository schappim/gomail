package gmail

import (
	"bytes"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// traceConn copies the IMAP conversation to a debug writer, one "C: " or
// "S: " line at a time. Credentials never reach the writer: the LOGIN command
// is replaced wholesale, and any occurrence of the password is masked.
type traceConn struct {
	net.Conn

	mu      sync.Mutex
	out     io.Writer
	secrets []string
	client  []byte // partial client line
	server  []byte // partial server line

	inLogin atomic.Bool
}

const redacted = "[redacted]"

func newTraceConn(conn net.Conn, out io.Writer, passwords ...string) *traceConn {
	t := &traceConn{Conn: conn, out: out}
	for _, p := range passwords {
		if len(p) >= 4 {
			t.secrets = append(t.secrets, strconv.Quote(p), p)
		}
	}
	return t
}

func (t *traceConn) redactLogin(on bool) { t.inLogin.Store(on) }

func (t *traceConn) Read(p []byte) (int, error) {
	n, err := t.Conn.Read(p)
	if n > 0 {
		t.trace(&t.server, "S: ", p[:n])
	}
	return n, err
}

func (t *traceConn) Write(p []byte) (int, error) {
	t.trace(&t.client, "C: ", p)
	return t.Conn.Write(p)
}

// trace buffers data and emits complete lines with the given prefix. It also
// serves as the go-imap error log writer (with a nil buffer).
func (t *traceConn) trace(buf *[]byte, prefix string, data []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	*buf = append(*buf, data...)
	for {
		i := bytes.IndexByte(*buf, '\n')
		if i < 0 {
			return
		}
		line := strings.TrimRight(string((*buf)[:i]), "\r")
		*buf = (*buf)[i+1:]
		if prefix == "C: " {
			line = t.scrub(line)
		}
		io.WriteString(t.out, prefix+line+"\n")
	}
}

func (t *traceConn) scrub(line string) string {
	if t.inLogin.Load() {
		if f := strings.Fields(line); len(f) >= 2 && strings.EqualFold(f[1], "LOGIN") {
			return f[0] + " LOGIN " + redacted
		}
		return redacted
	}
	for _, s := range t.secrets {
		line = strings.ReplaceAll(line, s, redacted)
	}
	return line
}

// logWriter adapts the trace for go-imap's ErrorLog.
type logWriter struct{ t *traceConn }

func (w logWriter) Write(p []byte) (int, error) {
	var buf []byte
	w.t.trace(&buf, "! ", p)
	return len(p), nil
}
