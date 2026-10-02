package compose

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"

	"gomail/internal/model"
)

// node is a parsed MIME part.
type node struct {
	header    message.Header
	mediaType string
	params    map[string]string
	children  []*node
	body      string // decoded, CRLF normalised to LF
}

func parseNode(t *testing.T, e *message.Entity) *node {
	t.Helper()
	mt, params, err := e.Header.ContentType()
	if err != nil {
		t.Fatalf("Content-Type %q: %v", e.Header.Get("Content-Type"), err)
	}
	n := &node{header: e.Header, mediaType: mt, params: params}
	if mr := e.MultipartReader(); mr != nil {
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("NextPart: %v", err)
			}
			n.children = append(n.children, parseNode(t, p))
		}
		return n
	}
	b, err := io.ReadAll(e.Body)
	if err != nil {
		t.Fatalf("reading %s body: %v", mt, err)
	}
	n.body = strings.ReplaceAll(string(b), "\r\n", "\n")
	return n
}

// parse reads a raw message back with go-message.
func parse(t *testing.T, raw []byte) (*node, *mail.Header) {
	t.Helper()
	e, err := message.Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("message.Read: %v\n%s", err, raw)
	}
	mr, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("mail.CreateReader: %v", err)
	}
	return parseNode(t, e), &mr.Header
}

func (n *node) structure() string {
	if len(n.children) == 0 && !strings.HasPrefix(n.mediaType, "multipart/") {
		return n.mediaType
	}
	parts := make([]string, len(n.children))
	for i, c := range n.children {
		parts[i] = c.structure()
	}
	return n.mediaType + "(" + strings.Join(parts, ",") + ")"
}

// find returns the first part (depth-first) with the media type.
func (n *node) find(mediaType string) *node {
	if n.mediaType == mediaType {
		return n
	}
	for _, c := range n.children {
		if f := c.find(mediaType); f != nil {
			return f
		}
	}
	return nil
}

func mustBuild(t *testing.T, o Options) *Built {
	t.Helper()
	b, err := Build(o)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return b
}

// checkLines asserts the wire format: CRLF line endings, encoded body lines
// within 76 characters and header lines within the 998 hard limit.
func checkLines(t *testing.T, raw []byte) {
	t.Helper()
	s := string(raw)
	if strings.Contains(strings.ReplaceAll(s, "\r\n", ""), "\n") {
		t.Errorf("bare LF in output")
	}
	inHeader := true
	for _, l := range strings.Split(s, "\r\n") {
		if l == "" {
			inHeader = false
			continue
		}
		if len(l) > 998 {
			t.Errorf("line over 998 characters: %.60q...", l)
		}
		if !inHeader && !strings.Contains(l, ":") && len(l) > 76 {
			t.Errorf("body line over 76 characters: %q", l)
		}
	}
}

func checkPart(t *testing.T, n *node, mediaType, cte string) {
	t.Helper()
	if n == nil {
		t.Fatalf("no %s part", mediaType)
	}
	if n.mediaType != mediaType {
		t.Errorf("media type = %q, want %q", n.mediaType, mediaType)
	}
	if got := n.header.Get("Content-Transfer-Encoding"); got != cte {
		t.Errorf("%s Content-Transfer-Encoding = %q, want %q", mediaType, got, cte)
	}
	if strings.HasPrefix(mediaType, "text/") && n.header.Get("Content-Disposition") == "" {
		if cs := n.params["charset"]; cs != "utf-8" {
			t.Errorf("%s charset = %q, want utf-8", mediaType, cs)
		}
	}
}

var (
	alice = model.Address{Name: "Alice Example", Email: "alice@example.com"}
	bob   = model.Address{Name: "Bob Smith", Email: "bob@example.com"}
	carol = model.Address{Name: "Carol", Email: "carol@example.com"}
	dave  = model.Address{Email: "dave@example.org"}

	quoteDate = time.Date(2026, 10, 2, 11, 15, 0, 0, time.Local)
	sendDate  = time.Date(2026, 10, 2, 12, 0, 0, 0, time.FixedZone("AEST", 10*3600))
)
