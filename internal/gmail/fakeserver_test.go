package gmail

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A scripted fake Gmail IMAP server. Each step names the exact command the
// client must send next (without its tag; literals appear inline as
// "{n}\r\n<data>") and the lines to answer with. Response lines starting with
// OK, NO or BAD are sent tagged; every other line is sent verbatim. A step
// with no tagged line is answered with "OK Success".
//
// CAPABILITY and LOGOUT are answered automatically and are not scripted.

type step struct {
	cmd  string
	resp []string
}

const (
	testUser     = "me@gmail.com"
	testPassword = "abcdefghijklmnop"

	// What Gmail advertises before and after authentication.
	preAuthCaps = "IMAP4rev1 UNSELECT IDLE NAMESPACE QUOTA ID XLIST CHILDREN X-GM-EXT-1 XYZZY SASL-IR AUTH=XOAUTH2 AUTH=PLAIN AUTH=PLAIN-CLIENTTOKEN AUTH=OAUTHBEARER AUTH=XOAUTH"
	gmailCaps   = "IMAP4rev1 UNSELECT IDLE NAMESPACE QUOTA ID XLIST CHILDREN X-GM-EXT-1 UIDPLUS COMPRESS=DEFLATE ENABLE MOVE CONDSTORE ESEARCH UTF8=ACCEPT LIST-EXTENDED LIST-STATUS LITERAL- SPECIAL-USE APPENDLIMIT=35651584"
)

type fakeServer struct {
	t    *testing.T
	ln   net.Listener
	caps string // post-login CAPABILITY

	mu       sync.Mutex
	steps    []step
	next     int
	received []string
	conn     net.Conn
	done     chan struct{}
}

func newFakeServer(t *testing.T, steps ...step) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeServer{t: t, ln: ln, caps: gmailCaps, steps: steps, done: make(chan struct{})}
	go f.serve()
	t.Cleanup(f.shutdown)
	return f
}

// newGmail starts a fake server whose script begins with a successful LOGIN
// and returns a client logged in to it.
func newGmail(t *testing.T, steps ...step) (*Client, *fakeServer) {
	t.Helper()
	f := newFakeServer(t, append([]step{loginStep()}, steps...)...)
	c, err := f.dial(Config{Username: testUser, Password: testPassword})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c, f
}

func loginStep() step {
	return step{
		cmd: `LOGIN "me@gmail.com" "abcdefghijklmnop"`,
		resp: []string{
			"* CAPABILITY " + gmailCaps,
			"OK me@gmail.com authenticated (Success)",
		},
	}
}

func (f *fakeServer) dial(cfg Config) (*Client, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	c, err := dial(cfg, func() (net.Conn, error) { return net.Dial("tcp", f.ln.Addr().String()) })
	if c != nil {
		c.sleep = func(time.Duration) {}
	}
	return c, err
}

// setCaps changes the post-login CAPABILITY list.
func (f *fakeServer) setCaps(caps string) {
	f.mu.Lock()
	f.caps = caps
	f.mu.Unlock()
}

// commands returns every command received so far, tags stripped.
func (f *fakeServer) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.received...)
}

func (f *fakeServer) shutdown() {
	f.ln.Close()
	f.mu.Lock()
	if f.conn != nil {
		f.conn.Close()
	}
	f.mu.Unlock()
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
		f.t.Error("fake server did not stop")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.steps[f.next:] {
		f.t.Errorf("expected command was never sent: %q", s.cmd)
	}
}

func (f *fakeServer) serve() {
	defer close(f.done)
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	f.mu.Lock()
	f.conn = conn
	f.mu.Unlock()
	defer conn.Close()

	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	send := func(lines ...string) {
		for _, l := range lines {
			w.WriteString(l + "\r\n")
		}
		w.Flush()
	}
	send("* OK [CAPABILITY " + preAuthCaps + "] Gimap ready for requests from 127.0.0.1 x1mb12345")

	for {
		line, err := readCommand(r, w)
		if err != nil {
			return
		}
		tag, cmd, _ := strings.Cut(line, " ")
		f.mu.Lock()
		f.received = append(f.received, cmd)
		f.mu.Unlock()

		switch strings.ToUpper(cmd) {
		case "CAPABILITY":
			f.mu.Lock()
			caps := f.caps
			f.mu.Unlock()
			send("* CAPABILITY "+caps, tag+" OK Success")
			continue
		case "LOGOUT":
			send("* BYE LOGOUT Requested", tag+" OK 73 good day (Success)")
			return
		}

		resp, ok := f.match(cmd)
		if !ok {
			send(tag + " BAD unexpected command")
			continue
		}
		tagged := false
		for _, l := range resp {
			if isTagged(l) {
				l = tag + " " + l
				tagged = true
			}
			send(l)
		}
		if !tagged {
			send(tag + " OK Success")
		}
	}
}

func (f *fakeServer) match(cmd string) ([]string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.next >= len(f.steps) {
		f.t.Errorf("unexpected command %q (script finished)", cmd)
		return nil, false
	}
	s := f.steps[f.next]
	if cmd != s.cmd {
		f.t.Errorf("command %d:\n got: %q\nwant: %q", f.next+1, cmd, s.cmd)
		return nil, false
	}
	f.next++
	return s.resp, true
}

func isTagged(l string) bool {
	for _, p := range []string{"OK", "NO", "BAD"} {
		if l == p || strings.HasPrefix(l, p+" ") {
			return true
		}
	}
	return false
}

var literalRE = regexp.MustCompile(`\{(\d+)(\+?)\}$`)

// readCommand reads one command, including any literals, and returns it
// without the final CRLF. A synchronizing literal gets a continuation request.
func readCommand(r *bufio.Reader, w *bufio.Writer) (string, error) {
	var sb strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		sb.WriteString(line)
		m := literalRE.FindStringSubmatch(line)
		if m == nil {
			return sb.String(), nil
		}
		n, _ := strconv.Atoi(m[1])
		if m[2] == "" {
			w.WriteString("+ go ahead\r\n")
			w.Flush()
		}
		data := make([]byte, n)
		if _, err := io.ReadFull(r, data); err != nil {
			return "", err
		}
		sb.WriteString("\r\n")
		sb.Write(data)
	}
}

// lit formats s as an IMAP literal.
func lit(s string) string { return fmt.Sprintf("{%d}\r\n%s", len(s), s) }

// Gmail fixtures.

var gmailList = []string{
	`* LIST (\HasNoChildren) "/" "INBOX"`,
	`* LIST (\HasChildren \Noselect) "/" "[Gmail]"`,
	`* LIST (\All \HasNoChildren) "/" "[Gmail]/All Mail"`,
	`* LIST (\Drafts \HasNoChildren) "/" "[Gmail]/Drafts"`,
	`* LIST (\HasNoChildren \Important) "/" "[Gmail]/Important"`,
	`* LIST (\HasNoChildren \Sent) "/" "[Gmail]/Sent Mail"`,
	`* LIST (\HasNoChildren \Junk) "/" "[Gmail]/Spam"`,
	`* LIST (\Flagged \HasNoChildren) "/" "[Gmail]/Starred"`,
	`* LIST (\HasNoChildren \Trash) "/" "[Gmail]/Trash"`,
	`* LIST (\HasNoChildren) "/" "Archive"`,
	`* LIST (\HasNoChildren) "/" "Caf&AOk-"`,
	`* LIST (\HasChildren) "/" "Clients"`,
	`* LIST (\HasNoChildren) "/" "Clients/Acme"`,
	`* LIST (\HasNoChildren) "/" "Receipts"`,
}

var googleMailList = []string{
	`* LIST (\HasNoChildren) "/" "INBOX"`,
	`* LIST (\HasChildren \Noselect) "/" "[Google Mail]"`,
	`* LIST (\All \HasNoChildren) "/" "[Google Mail]/Alle Nachrichten"`,
	`* LIST (\Drafts \HasNoChildren) "/" "[Google Mail]/Entw&APw-rfe"`,
	`* LIST (\HasNoChildren \Sent) "/" "[Google Mail]/Gesendet"`,
	`* LIST (\HasNoChildren \Junk) "/" "[Google Mail]/Spam"`,
	`* LIST (\Flagged \HasNoChildren) "/" "[Google Mail]/Markiert"`,
	`* LIST (\HasNoChildren \Trash) "/" "[Google Mail]/Papierkorb"`,
	`* LIST (\HasNoChildren \Important) "/" "[Google Mail]/Wichtig"`,
}

func listStep(lines []string) step {
	return step{cmd: `LIST "" "*"`, resp: append(append([]string(nil), lines...), "OK Success")}
}

// selectStep answers SELECT (writable) or EXAMINE the way Gmail does.
func selectStep(verb, wireName string, exists int) step {
	mode := "READ-WRITE"
	if verb == "EXAMINE" {
		mode = "READ-ONLY"
	}
	return step{
		cmd: verb + " " + wireName,
		resp: []string{
			`* FLAGS (\Answered \Flagged \Draft \Deleted \Seen $NotPhishing $Phishing)`,
			`* OK [PERMANENTFLAGS (\Answered \Flagged \Draft \Deleted \Seen $NotPhishing $Phishing \*)] Flags permitted.`,
			`* OK [UIDVALIDITY 11] UIDs valid.`,
			fmt.Sprintf("* %d EXISTS", exists),
			`* 0 RECENT`,
			`* OK [UIDNEXT 5000] Predicted next UID.`,
			`* OK [HIGHESTMODSEQ 9876543]`,
			fmt.Sprintf("OK [%s] %s selected. (Success)", mode, strings.Trim(wireName, `"`)),
		},
	}
}

func searchStep(cmd string, uids ...int) step {
	resp := "* SEARCH"
	for _, u := range uids {
		resp += " " + strconv.Itoa(u)
	}
	return step{cmd: cmd, resp: []string{resp, "OK SEARCH completed (Success)"}}
}

const fetchSummaryItems = "(ENVELOPE FLAGS INTERNALDATE RFC822.SIZE UID BODYSTRUCTURE X-GM-MSGID X-GM-THRID X-GM-LABELS)"

// Body structures as Gmail sends them.
const (
	bsPlain = `("TEXT" "PLAIN" ("CHARSET" "UTF-8") NIL NIL "7BIT" 120 4 NIL NIL NIL)`
	bsHTML  = `("TEXT" "HTML" ("CHARSET" "UTF-8") NIL NIL "QUOTED-PRINTABLE" 400 10 NIL NIL NIL)`
	bsAlt   = `(("TEXT" "PLAIN" ("CHARSET" "UTF-8") NIL NIL "7BIT" 50 2 NIL NIL NIL)("TEXT" "HTML" ("CHARSET" "UTF-8") NIL NIL "QUOTED-PRINTABLE" 300 8 NIL NIL NIL) "ALTERNATIVE" ("BOUNDARY" "000000000000b1") NIL NIL)`
	bsPDF   = `((("TEXT" "PLAIN" ("CHARSET" "UTF-8") NIL NIL "7BIT" 50 2 NIL NIL NIL)("TEXT" "HTML" ("CHARSET" "UTF-8") NIL NIL "QUOTED-PRINTABLE" 300 8 NIL NIL NIL) "ALTERNATIVE" ("BOUNDARY" "000000000000a2") NIL NIL)("APPLICATION" "PDF" ("NAME" "invoice.pdf") "<f_lq1x2y3z0>" NIL "BASE64" 52000 NIL ("ATTACHMENT" ("FILENAME" "invoice.pdf")) NIL) "MIXED" ("BOUNDARY" "000000000000a1") NIL NIL)`
	bsLogo  = `((("TEXT" "PLAIN" ("CHARSET" "UTF-8") NIL NIL "7BIT" 50 2 NIL NIL NIL)("TEXT" "HTML" ("CHARSET" "UTF-8") NIL NIL "QUOTED-PRINTABLE" 300 8 NIL NIL NIL) "ALTERNATIVE" ("BOUNDARY" "b2") NIL NIL)("IMAGE" "PNG" ("NAME" "logo.png") "<logo@acme.example>" NIL "BASE64" 4000 NIL ("INLINE" ("FILENAME" "logo.png")) NIL) "RELATED" ("BOUNDARY" "b1") NIL NIL)`
)

// fetchLine renders a FETCH response in Gmail's item order. env is a full
// ENVELOPE list; extra is appended inside the parentheses.
func fetchLine(seq, uid int, msgid, thrid uint64, labels, flags, env, bs, extra string) string {
	return fmt.Sprintf(`* %d FETCH (X-GM-THRID %d X-GM-MSGID %d X-GM-LABELS (%s) UID %d RFC822.SIZE 2345 INTERNALDATE "01-Oct-2026 09:30:00 +0000" FLAGS (%s) ENVELOPE %s BODYSTRUCTURE %s%s)`,
		seq, thrid, msgid, labels, uid, flags, env, bs, extra)
}

// envelope renders an ENVELOPE with one sender and one recipient.
func envelope(date, subject, messageID string) string {
	return fmt.Sprintf(`("%s" "%s" (("Bob Smith" NIL "bob" "example.com")) (("Bob Smith" NIL "bob" "example.com")) (("Bob Smith" NIL "bob" "example.com")) ((NIL NIL "me" "gmail.com")) NIL NIL NIL "<%s>")`,
		date, subject, messageID)
}
