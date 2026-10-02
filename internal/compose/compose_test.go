package compose

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/emersion/go-message/mail"

	"gomail/internal/htmltext"
	"gomail/internal/model"
)

func TestBuildTextOnly(t *testing.T) {
	o := Options{
		From:    model.Address{Name: "José Müller", Email: "jose@example.com"},
		To:      []model.Address{{Name: "Last, First", Email: "first@example.com"}, dave},
		Cc:      []model.Address{carol},
		Subject: "Grüße aus München – a rather long subject line that has to be encoded and folded",
		Text:    "Hello there,\r\n\r\nA line with a trailing space \nand a very long line that goes on and on well past the seventy-six character limit for quoted-printable.\n\n",
		Date:    sendDate,
		Headers: []model.Header{{Name: "X-Tracking", Value: "none"}},
	}
	b := mustBuild(t, o)
	checkLines(t, b.Raw)

	if b.Structure != "text/plain" {
		t.Errorf("Structure = %q", b.Structure)
	}
	root, h := parse(t, b.Raw)
	if got := root.structure(); got != b.Structure {
		t.Errorf("parsed structure %q != Built.Structure %q", got, b.Structure)
	}
	checkPart(t, root, "text/plain", "quoted-printable")

	wantText := "Hello there,\n\nA line with a trailing space \nand a very long line that goes on and on well past the seventy-six character limit for quoted-printable.\n"
	if root.body != wantText {
		t.Errorf("decoded body = %q, want %q", root.body, wantText)
	}
	if b.Text != wantText || b.HTML != "" {
		t.Errorf("Built.Text = %q, Built.HTML = %q", b.Text, b.HTML)
	}

	if subj, err := h.Subject(); err != nil || subj != o.Subject {
		t.Errorf("Subject = %q (%v), want %q", subj, err, o.Subject)
	}
	if !bytes.Contains(b.Raw, []byte("Subject: =?utf-8?q?")) {
		t.Errorf("Subject not RFC 2047 encoded:\n%s", b.Raw)
	}
	from, err := h.AddressList("From")
	if err != nil || len(from) != 1 || from[0].Name != "José Müller" || from[0].Address != "jose@example.com" {
		t.Errorf("From = %v (%v)", from, err)
	}
	to, err := h.AddressList("To")
	if err != nil || len(to) != 2 || to[0].Name != "Last, First" || to[1].Address != "dave@example.org" {
		t.Errorf("To = %v (%v)", to, err)
	}
	if d, err := h.Date(); err != nil || !d.Equal(sendDate) {
		t.Errorf("Date = %v (%v)", d, err)
	}
	id, err := h.MessageID()
	if err != nil || id != b.MessageID || !strings.HasSuffix(id, "@example.com") || len(id) < 20 {
		t.Errorf("Message-ID = %q (%v), Built.MessageID = %q", id, err, b.MessageID)
	}
	for _, want := range []string{"MIME-Version: 1.0\r\n", "User-Agent: gomail\r\n", "X-Tracking: none\r\n", "Message-ID: <"} {
		if !bytes.Contains(b.Raw, []byte(want)) {
			t.Errorf("missing %q", want)
		}
	}
	if !bytes.HasPrefix(b.Raw, []byte("From: ")) {
		t.Errorf("header should start with From:\n%s", b.Raw[:80])
	}
	if h.Get("Bcc") != "" || h.Get("In-Reply-To") != "" || h.Get("References") != "" {
		t.Errorf("unexpected headers present")
	}
	if want := []string{"first@example.com", "dave@example.org", "carol@example.com"}; strings.Join(b.Recipients, ",") != strings.Join(want, ",") {
		t.Errorf("Recipients = %v, want %v", b.Recipients, want)
	}
}

func TestBuildHTMLOnlyDerivesText(t *testing.T) {
	src := "<p>Hello <b>world</b></p>"
	b := mustBuild(t, Options{From: alice, To: []model.Address{bob}, Subject: "hi", HTML: src})
	checkLines(t, b.Raw)
	if b.Structure != "multipart/alternative(text/plain,text/html)" {
		t.Fatalf("Structure = %q", b.Structure)
	}
	root, _ := parse(t, b.Raw)
	if root.structure() != b.Structure {
		t.Errorf("parsed structure = %q", root.structure())
	}
	text, html := root.children[0], root.children[1]
	checkPart(t, text, "text/plain", "quoted-printable")
	checkPart(t, html, "text/html", "quoted-printable")

	wantHTML := `<!DOCTYPE html><html><head><meta charset="utf-8"></head><body><div dir="ltr">` + src + `</div></body></html>`
	if html.body != wantHTML || b.HTML != wantHTML {
		t.Errorf("html = %q\nwant  %q", html.body, wantHTML)
	}
	wantText := strings.TrimRight(htmltext.Convert(src), " \t\n") + "\n"
	if text.body != wantText || b.Text != wantText {
		t.Errorf("text = %q, want %q", text.body, wantText)
	}
}

func TestBuildMarkdown(t *testing.T) {
	md := "# Title\n\nSome **bold** and ~~struck~~ text\nwith a line break.\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n- [x] done\n- [ ] todo\n\nSee https://example.com/docs\n"
	b := mustBuild(t, Options{From: alice, To: []model.Address{bob}, Subject: "md", Markdown: md})
	if b.Structure != "multipart/alternative(text/plain,text/html)" {
		t.Fatalf("Structure = %q", b.Structure)
	}
	root, _ := parse(t, b.Raw)
	if got := root.children[0].body; got != md {
		t.Errorf("text part should be the Markdown source, got %q", got)
	}
	html := root.children[1].body
	for _, want := range []string{
		"<h1>Title</h1>",
		"<strong>bold</strong>",
		"<del>struck</del>",
		"text<br>",
		"<table>",
		`type="checkbox"`,
		`<a href="https://example.com/docs">https://example.com/docs</a>`,
		`<div dir="ltr">`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML missing %q:\n%s", want, html)
		}
	}

	for _, o := range []Options{
		{From: alice, Markdown: "x", Text: "y"},
		{From: alice, Markdown: "x", HTML: "<p>y</p>"},
	} {
		if _, err := Build(o); err == nil || !strings.Contains(err.Error(), "Markdown") {
			t.Errorf("Markdown combined with another body: err = %v", err)
		}
	}
}

func TestBuildTextAndHTML(t *testing.T) {
	b := mustBuild(t, Options{From: alice, To: []model.Address{bob}, Text: "plain version", HTML: "<p>rich version</p>"})
	root, _ := parse(t, b.Raw)
	if root.structure() != "multipart/alternative(text/plain,text/html)" {
		t.Fatalf("structure = %q", root.structure())
	}
	if root.children[0].body != "plain version\n" {
		t.Errorf("text = %q", root.children[0].body)
	}
	if !strings.Contains(root.children[1].body, "<p>rich version</p>") {
		t.Errorf("html = %q", root.children[1].body)
	}
}

func TestBuildAttachments(t *testing.T) {
	pdf := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte{0, 1, 2, 250, 251, 252}, 100)...)
	longName := strings.Repeat("Überweisungsbestätigung ", 4) + "März 2026.pdf"
	o := Options{
		From: alice, To: []model.Address{bob}, Subject: "files", Text: "See attached.",
		Attachments: []File{
			{Filename: "Résumé – 2026.pdf", ContentType: "application/pdf", Data: pdf},
			{Filename: "notes.txt", Data: []byte("just some notes\n")},
			{Filename: longName, Data: []byte("x")},
			{Filename: `quote"d name.csv`, Data: []byte("a,b\n")},
		},
	}
	b := mustBuild(t, o)
	checkLines(t, b.Raw)
	want := "multipart/mixed(text/plain,application/pdf,text/plain,application/pdf,text/csv)"
	if b.Structure != want {
		t.Fatalf("Structure = %q, want %q", b.Structure, want)
	}
	root, _ := parse(t, b.Raw)
	if root.structure() != want {
		t.Fatalf("parsed structure = %q", root.structure())
	}
	if root.children[0].body != "See attached.\n" {
		t.Errorf("text = %q", root.children[0].body)
	}

	for i, f := range o.Attachments {
		p := root.children[i+1]
		checkPart(t, p, p.mediaType, "base64")
		ah := mail.AttachmentHeader{Header: p.header}
		name, err := ah.Filename()
		if err != nil || name != f.Filename {
			t.Errorf("attachment %d filename = %q (%v), want %q", i, name, err, f.Filename)
		}
		if disp, _, _ := p.header.ContentDisposition(); disp != "attachment" {
			t.Errorf("attachment %d disposition = %q", i, disp)
		}
		if p.body != strings.ReplaceAll(string(f.Data), "\r\n", "\n") {
			t.Errorf("attachment %d data mismatch", i)
		}
		if _, params, _ := p.header.ContentType(); params["name"] != f.Filename {
			t.Errorf("attachment %d Content-Type name = %q", i, params["name"])
		}
		meta := b.Attachments[i]
		if meta.Index != i+1 || meta.Filename != f.Filename || meta.Size != len(f.Data) || meta.Inline || meta.ContentType != p.mediaType {
			t.Errorf("attachment %d metadata = %+v", i, meta)
		}
	}
	if !bytes.Contains(b.Raw, []byte("filename*=utf-8''R%C3%A9sum%C3%A9%20%E2%80%93%202026.pdf")) {
		t.Errorf("non-ASCII filename not RFC 2231 encoded:\n%s", b.Raw)
	}
	if !bytes.Contains(b.Raw, []byte("filename*0*=utf-8''")) || !bytes.Contains(b.Raw, []byte("filename*1*=")) {
		t.Errorf("long filename not split into RFC 2231 continuations")
	}
	if b.Attachments[1].ContentType != "text/plain" {
		t.Errorf("detected content type = %q", b.Attachments[1].ContentType)
	}
}

func TestBuildInlineImage(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nfake image data")
	o := Options{
		From: alice, To: []model.Address{bob}, Subject: "logo",
		HTML: `<p>Our logo: <img src="cid:logo123@example.com"></p>`,
		Attachments: []File{
			{Filename: "report.pdf", ContentType: "application/pdf", Data: []byte("%PDF")},
			{Filename: "logo.png", ContentType: "image/png", Data: png, ContentID: "<logo123@example.com>", Inline: true},
		},
	}
	b := mustBuild(t, o)
	want := "multipart/mixed(multipart/alternative(text/plain,multipart/related(text/html,image/png)),application/pdf)"
	if b.Structure != want {
		t.Fatalf("Structure = %q, want %q", b.Structure, want)
	}
	root, _ := parse(t, b.Raw)
	if root.structure() != want {
		t.Fatalf("parsed structure = %q", root.structure())
	}
	rel := root.find("multipart/related")
	if rel.params["type"] != "text/html" {
		t.Errorf("multipart/related type = %q", rel.params["type"])
	}
	img := root.find("image/png")
	checkPart(t, img, "image/png", "base64")
	if got := img.header.Get("Content-ID"); got != "<logo123@example.com>" {
		t.Errorf("Content-ID = %q", got)
	}
	if disp, params, _ := img.header.ContentDisposition(); disp != "inline" || params["filename"] != "logo.png" {
		t.Errorf("image disposition = %q %v", disp, params)
	}
	if img.body != strings.ReplaceAll(string(png), "\r\n", "\n") {
		t.Errorf("image data mismatch")
	}
	// Metadata follows MIME order: the inline image comes first.
	if len(b.Attachments) != 2 ||
		!reflect.DeepEqual(b.Attachments[0], model.Attachment{Index: 1, Filename: "logo.png", ContentType: "image/png", Size: len(png), ContentID: "logo123@example.com", Inline: true}) ||
		b.Attachments[1].Index != 2 || b.Attachments[1].Filename != "report.pdf" {
		t.Errorf("Attachments = %+v", b.Attachments)
	}

	// Without an HTML part there's nothing to relate the image to.
	o.HTML, o.Text = "", "text only"
	b = mustBuild(t, o)
	if b.Structure != "multipart/mixed(text/plain,application/pdf,image/png)" {
		t.Errorf("text-only Structure = %q", b.Structure)
	}
	if !b.Attachments[1].Inline {
		t.Errorf("inline image lost its disposition: %+v", b.Attachments[1])
	}
}

func TestBuildBcc(t *testing.T) {
	o := Options{
		From: alice, To: []model.Address{bob}, Cc: []model.Address{{Email: "BOB@example.com"}},
		Bcc: []model.Address{carol}, Subject: "bcc", Text: "hi",
	}
	sent := mustBuild(t, o)
	if bytes.Contains(sent.Raw, []byte("Bcc:")) || bytes.Contains(sent.Raw, []byte("carol@")) {
		t.Errorf("Bcc leaked into a sent message:\n%s", sent.Raw)
	}
	o.IncludeBcc = true
	draft := mustBuild(t, o)
	_, h := parse(t, draft.Raw)
	if bcc, err := h.AddressList("Bcc"); err != nil || len(bcc) != 1 || bcc[0].Address != "carol@example.com" {
		t.Errorf("draft Bcc = %v (%v)", bcc, err)
	}
	want := "bob@example.com,carol@example.com"
	for _, b := range []*Built{sent, draft} {
		if got := strings.Join(b.Recipients, ","); got != want {
			t.Errorf("Recipients = %q, want %q", got, want)
		}
	}
}

func TestBuildNonASCIINamesAndSubject(t *testing.T) {
	o := Options{
		From:    model.Address{Name: "Zoë \"Z\" O'Brien", Email: "zoe@example.com"},
		To:      []model.Address{{Name: "山田 太郎", Email: "taro@example.jp"}, {Name: "Smith, John (Sales)", Email: "john@example.com"}},
		ReplyTo: []model.Address{{Name: "Équipe", Email: "team@example.com"}},
		Subject: "日本語の件名 – émoji 🎉",
		Text:    "ünïcödé body ✓",
	}
	b := mustBuild(t, o)
	checkLines(t, b.Raw)
	root, h := parse(t, b.Raw)
	if s, _ := h.Subject(); s != o.Subject {
		t.Errorf("Subject = %q", s)
	}
	check := func(key string, want []model.Address) {
		t.Helper()
		got, err := h.AddressList(key)
		if err != nil || len(got) != len(want) {
			t.Fatalf("%s = %v (%v)", key, got, err)
		}
		for i := range want {
			if got[i].Name != want[i].Name || got[i].Address != want[i].Email {
				t.Errorf("%s[%d] = %q <%s>, want %q <%s>", key, i, got[i].Name, got[i].Address, want[i].Name, want[i].Email)
			}
		}
	}
	check("From", []model.Address{o.From})
	check("To", o.To)
	check("Reply-To", o.ReplyTo)
	if root.body != "ünïcödé body ✓\n" {
		t.Errorf("body = %q", root.body)
	}
	header := string(b.Raw[:bytes.Index(b.Raw, []byte("\r\n\r\n"))])
	for i := 0; i < len(header); i++ {
		if header[i] >= 0x80 {
			t.Fatalf("raw 8-bit byte in header:\n%s", header)
		}
	}
}

func TestBuildFullHTMLDocument(t *testing.T) {
	doc := "<html><head><style>p{color:red}</style></head><body class=\"x\"><p>Hi</p></body></html>"
	b := mustBuild(t, Options{From: alice, HTML: doc, Signature: &Signature{HTML: "<b>Alice</b>"}})
	want := `<html><head><style>p{color:red}</style></head><body class="x"><p>Hi</p><br><div class="gmail_signature"><b>Alice</b></div></body></html>`
	if b.HTML != want {
		t.Errorf("HTML = %q\nwant   %q", b.HTML, want)
	}
}

func TestBuildErrors(t *testing.T) {
	cases := []struct {
		name string
		o    Options
		want string
	}{
		{"no body", Options{From: alice, To: []model.Address{bob}}, "no body"},
		{"no from", Options{To: []model.Address{bob}, Text: "x"}, "From"},
		{"bad to", Options{From: alice, To: []model.Address{{Email: "not an email"}}, Text: "x"}, "invalid email"},
		{"reserved header", Options{From: alice, Text: "x", Headers: []model.Header{{Name: "Bcc", Value: "x@y.com"}}}, "Options.Bcc"},
		{"header injection", Options{From: alice, Text: "x", Headers: []model.Header{{Name: "X-A", Value: "1\r\nBcc: x@y.com"}}}, "line break"},
		{"bad content type", Options{From: alice, Text: "x", Attachments: []File{{Filename: "a", ContentType: "not a type;;", Data: []byte("x")}}}, "content type"},
	}
	for _, c := range cases {
		if _, err := Build(c.o); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.want)
		}
	}

	// Attachments alone, or a quote alone, are enough.
	if _, err := Build(Options{From: alice, Attachments: []File{{Filename: "a.txt", Data: []byte("x")}}}); err != nil {
		t.Errorf("attachment-only message: %v", err)
	}
	if _, err := Build(Options{From: alice, Quote: &model.Message{Text: "orig"}, Forward: true}); err != nil {
		t.Errorf("forward without a body: %v", err)
	}
}

func TestAttachmentLimit(t *testing.T) {
	big := make([]byte, MaxAttachmentBytes/2+1)
	_, err := Build(Options{From: alice, Text: "x", Attachments: []File{{Filename: "a.bin", Data: big}, {Filename: "b.bin", Data: big}}})
	if err == nil || !strings.Contains(err.Error(), "25 MB") {
		t.Fatalf("err = %v, want Gmail's 25 MB limit", err)
	}
	if _, err := Build(Options{From: alice, Text: "x", Attachments: []File{{Filename: "a.bin", Data: big}}}); err != nil {
		t.Errorf("12.5 MB attachment rejected: %v", err)
	}
}

func TestExtraHeadersOverride(t *testing.T) {
	b := mustBuild(t, Options{From: alice, Text: "x", Headers: []model.Header{
		{Name: "User-Agent", Value: "custom/1.0"},
		{Name: "X-Label", Value: "one"},
		{Name: "X-Label", Value: "twö"},
	}})
	_, h := parse(t, b.Raw)
	if got := h.Values("User-Agent"); len(got) != 1 || got[0] != "custom/1.0" {
		t.Errorf("User-Agent = %v", got)
	}
	if got := h.Values("X-Label"); len(got) != 2 {
		t.Errorf("X-Label = %v", got)
	}
	if v, _ := h.Text("X-Label"); v != "one" {
		t.Errorf("X-Label first = %q", v)
	}
	if !bytes.Contains(b.Raw, []byte("X-Label: =?utf-8?q?tw=C3=B6?=")) {
		t.Errorf("non-ASCII extra header not encoded")
	}
}

func TestMessageIDAndThreadingHeaders(t *testing.T) {
	b := mustBuild(t, Options{
		From: model.Address{Email: "me@Mail.Example.COM"}, Text: "x",
		InReplyTo: "<parent@x>", References: []string{"a@x", "<b@x> <parent@x>", "a@x"},
	})
	_, h := parse(t, b.Raw)
	if !strings.HasSuffix(b.MessageID, "@mail.example.com") {
		t.Errorf("MessageID = %q", b.MessageID)
	}
	if ids, _ := h.MsgIDList("In-Reply-To"); len(ids) != 1 || ids[0] != "parent@x" {
		t.Errorf("In-Reply-To = %v", ids)
	}
	if ids, _ := h.MsgIDList("References"); strings.Join(ids, " ") != "a@x b@x parent@x" {
		t.Errorf("References = %v", ids)
	}

	b = mustBuild(t, Options{From: alice, Text: "x", MessageID: "<fixed@id>"})
	if b.MessageID != "fixed@id" || !bytes.Contains(b.Raw, []byte("Message-ID: <fixed@id>\r\n")) {
		t.Errorf("explicit MessageID = %q", b.MessageID)
	}
	if got := messageIDDomain("weird@exa mple"); got != "gomail.local" {
		t.Errorf("messageIDDomain = %q", got)
	}
}

func TestFoldField(t *testing.T) {
	v := strings.Repeat("word ", 40) + "end"
	raw := string(foldField("X-Long", v))
	for _, l := range strings.Split(strings.TrimSuffix(raw, "\r\n"), "\r\n") {
		if len(l) > 76 {
			t.Errorf("line too long: %q", l)
		}
	}
	if unfolded := strings.ReplaceAll(strings.TrimSuffix(raw, "\r\n"), "\r\n", ""); unfolded != "X-Long: "+v {
		t.Errorf("unfolding changed the value: %q", unfolded)
	}
}

func TestPercentEncodedContinuationsDontSplitEscapes(t *testing.T) {
	name := strings.Repeat("é", 50) + ".txt"
	d := formatDisposition("attachment", name)
	for _, seg := range strings.Split(d, "; ")[1:] {
		val := seg[strings.IndexByte(seg, '=')+1:]
		val = strings.TrimPrefix(val, "utf-8''")
		for i := 0; i < len(val); i++ {
			if val[i] == '%' && i+2 >= len(val) {
				t.Fatalf("escape split across continuations: %q", seg)
			}
		}
	}
	var hdr mail.AttachmentHeader
	hdr.Set("Content-Disposition", d)
	if got, err := hdr.Filename(); err != nil || got != name {
		t.Errorf("round trip = %q (%v)", got, err)
	}
}

func TestErrorsAreNotWrappedPanics(t *testing.T) {
	// A nil quote/forward combination must not panic.
	if _, err := Build(Options{From: alice, Forward: true, Text: "x"}); err != nil {
		t.Errorf("forward flag without quote: %v", err)
	}
	var target interface{ Error() string }
	if _, err := Build(Options{}); !errors.As(err, &target) {
		t.Errorf("expected an error for empty options")
	}
}
