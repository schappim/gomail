package compose

import (
	"strings"
	"testing"

	"gomail/internal/htmltext"
	"gomail/internal/model"
)

func origMessage() *model.Message {
	return &model.Message{
		MessageSummary: model.MessageSummary{
			MessageID: "orig-2@example.com",
			InReplyTo: "orig-1@example.com",
			Date:      quoteDate,
			From:      []model.Address{bob},
			To:        []model.Address{alice, {Email: "dave@example.org"}},
			Cc:        []model.Address{carol, {Name: "Alice again", Email: "ALICE@example.com"}},
			Subject:   "Lunch plans",
		},
		References: []string{"orig-0@example.com", "orig-1@example.com"},
		Text:       "Hi there\n\n> earlier text\nbye\n",
		HTML:       `<html><head><title>x</title></head><body><div dir="ltr">Hi there<br><blockquote>earlier text</blockquote>bye</div></body></html>`,
	}
}

const attribution = "On Fri, 2 Oct 2026 at 11:15, Bob Smith <bob@example.com> wrote:"

func TestReplySignatureAboveQuote(t *testing.T) {
	orig := origMessage()
	o := Reply(orig, []string{"alice@example.com"}, false)
	o.From = alice
	o.Text = "Sounds good.\n"
	o.HTML = "<p>Sounds good.</p>"
	o.Signature = &Signature{Text: "-- \nAlice", HTML: "<b>Alice</b>"}
	b := mustBuild(t, o)

	wantText := "Sounds good.\n\n-- \nAlice\n\n" + attribution + "\n> Hi there\n>\n>> earlier text\n> bye\n"
	if b.Text != wantText {
		t.Errorf("text =\n%s\nwant\n%s", b.Text, wantText)
	}
	root, _ := parse(t, b.Raw)
	if got := root.find("text/plain").body; got != wantText {
		t.Errorf("decoded text part = %q", got)
	}

	html := root.find("text/html").body
	if html != b.HTML {
		t.Errorf("Built.HTML differs from the part")
	}
	sig := strings.Index(html, `<div class="gmail_signature"><b>Alice</b></div>`)
	quote := strings.Index(html, `<div class="gmail_quote">`)
	if sig < 0 || quote < 0 || sig > quote {
		t.Fatalf("signature (%d) must come before the quote (%d):\n%s", sig, quote, html)
	}
	wantQuote := `<div class="gmail_quote"><div dir="ltr" class="gmail_attr">On Fri, 2 Oct 2026 at 11:15, Bob Smith &lt;<a href="mailto:bob@example.com">bob@example.com</a>&gt; wrote:<br></div>` +
		`<blockquote class="gmail_quote" style="margin:0px 0px 0px 0.8ex;border-left:1px solid rgb(204,204,204);padding-left:1ex">` +
		`<div dir="ltr">Hi there<br><blockquote>earlier text</blockquote>bye</div></blockquote></div>`
	if !strings.Contains(html, wantQuote) {
		t.Errorf("HTML quote:\n%s\nwant it to contain\n%s", html, wantQuote)
	}
	if strings.Contains(html, "<title>") {
		t.Errorf("quoted the original's <head>")
	}
	if !strings.HasPrefix(html, htmlDocStart+`<div dir="ltr"><p>Sounds good.</p><br><div class="gmail_signature">`) || !strings.HasSuffix(html, htmlDocEnd) {
		t.Errorf("HTML wrapper wrong:\n%s", html)
	}
}

func TestReplyTextOnlyHasNoHTML(t *testing.T) {
	o := Reply(origMessage(), nil, false)
	o.From, o.Text = alice, "ok"
	b := mustBuild(t, o)
	if b.Structure != "text/plain" || b.HTML != "" {
		t.Errorf("text-only reply: Structure = %q", b.Structure)
	}
	if !strings.Contains(b.Text, "ok\n\n"+attribution+"\n> Hi there\n") {
		t.Errorf("text = %q", b.Text)
	}
}

func TestReplyHTMLQuotesPlainOriginalAndEscapes(t *testing.T) {
	orig := &model.Message{
		MessageSummary: model.MessageSummary{
			Date: quoteDate,
			From: []model.Address{{Name: `Eve <script>alert(1)</script>`, Email: "eve@example.com"}},
		},
		Text: "a < b & c\nsecond line",
	}
	o := Reply(orig, nil, false)
	o.From, o.HTML = alice, "<p>reply</p>"
	b := mustBuild(t, o)
	if strings.Contains(b.HTML, "<script>") {
		t.Fatalf("name not escaped:\n%s", b.HTML)
	}
	if !strings.Contains(b.HTML, "Eve &lt;script&gt;alert(1)&lt;/script&gt; &lt;<a href=\"mailto:eve@example.com\">") {
		t.Errorf("attribution:\n%s", b.HTML)
	}
	if !strings.Contains(b.HTML, `padding-left:1ex">a &lt; b &amp; c<br>second line</blockquote>`) {
		t.Errorf("plain original not escaped into the blockquote:\n%s", b.HTML)
	}
}

func TestSignatureDerivation(t *testing.T) {
	// Text-only signature, HTML message: HTML is derived with links.
	b := mustBuild(t, Options{From: alice, HTML: "<p>Hi</p>", Signature: &Signature{
		Text: "Alice & Co\nhttps://example.com/a_(b).\nalice@example.com",
	}})
	wantSig := `<div class="gmail_signature">Alice &amp; Co<br><a href="https://example.com/a_(b)">https://example.com/a_(b)</a>.<br><a href="mailto:alice@example.com">alice@example.com</a></div>`
	if !strings.Contains(b.HTML, wantSig) {
		t.Errorf("derived signature HTML:\n%s\nwant\n%s", b.HTML, wantSig)
	}
	if !strings.HasSuffix(b.Text, "\n\nAlice & Co\nhttps://example.com/a_(b).\nalice@example.com\n") {
		t.Errorf("text signature = %q", b.Text)
	}

	// HTML-only signature, text message: text is derived with htmltext.
	sigHTML := "<b>Alice</b>"
	b = mustBuild(t, Options{From: alice, Text: "Hi", Signature: &Signature{HTML: sigHTML}})
	want := "Hi\n\n" + strings.TrimSpace(htmltext.Convert(sigHTML)) + "\n"
	if b.Text != want {
		t.Errorf("text with derived signature = %q, want %q", b.Text, want)
	}
	if b.HTML != "" {
		t.Errorf("text message grew an HTML part")
	}
}

func TestForwardBlock(t *testing.T) {
	orig := origMessage()
	o := Forward(orig, false)
	o.From, o.To, o.Text = alice, []model.Address{dave}, "FYI"
	o.Signature = &Signature{Text: "Alice"}
	b := mustBuild(t, o)
	wantText := "FYI\n\nAlice\n\n---------- Forwarded message ---------\n" +
		"From: Bob Smith <bob@example.com>\n" +
		"Date: Fri, 2 Oct 2026 at 11:15\n" +
		"Subject: Lunch plans\n" +
		"To: Alice Example <alice@example.com>, <dave@example.org>\n" +
		"Cc: Carol <carol@example.com>, Alice again <ALICE@example.com>\n" +
		"\nHi there\n\n> earlier text\nbye\n"
	if b.Text != wantText {
		t.Errorf("forward text =\n%q\nwant\n%q", b.Text, wantText)
	}
	// The original has HTML, so a plain-text note still gets an HTML part
	// that carries the forwarded HTML.
	if b.Structure != "multipart/alternative(text/plain,text/html)" {
		t.Errorf("Structure = %q", b.Structure)
	}
	if !strings.Contains(b.HTML, "FYI") || !strings.Contains(b.HTML, "<blockquote>earlier text</blockquote>") {
		t.Errorf("forward HTML lost the note or the original:\n%s", b.HTML)
	}

	// No body of its own: allowed, and it keeps the original's HTML.
	o = Forward(orig, false)
	o.From, o.To = alice, []model.Address{dave}
	b = mustBuild(t, o)
	if !strings.HasPrefix(b.Text, "---------- Forwarded message ---------\nFrom: ") {
		t.Errorf("bare forward text = %q", b.Text)
	}
	if b.Structure != "multipart/alternative(text/plain,text/html)" {
		t.Fatalf("bare forward Structure = %q", b.Structure)
	}
	for _, want := range []string{
		`<div class="gmail_quote"><div dir="ltr" class="gmail_attr">---------- Forwarded message ---------<br>`,
		`From: <strong class="gmail_sendername" dir="auto">Bob Smith</strong> <span dir="auto">&lt;<a href="mailto:bob@example.com">bob@example.com</a>&gt;</span><br>`,
		`Subject: Lunch plans<br>`,
		`</div><br><br><div dir="ltr">Hi there<br><blockquote>earlier text</blockquote>bye</div></div>`,
	} {
		if !strings.Contains(b.HTML, want) {
			t.Errorf("forward HTML missing %q:\n%s", want, b.HTML)
		}
	}
	if strings.Contains(b.HTML, "border-left") {
		t.Errorf("forward block should not have the reply border")
	}
}

func TestReplyHeaders(t *testing.T) {
	self := []string{"Alice@Example.com"}
	orig := origMessage()

	r := Reply(orig, self, false)
	if got := model.FormatAddresses(r.To); got != "Bob Smith <bob@example.com>" || len(r.Cc) != 0 {
		t.Errorf("reply To = %q Cc = %v", got, r.Cc)
	}
	if r.Subject != "Re: Lunch plans" || r.InReplyTo != "orig-2@example.com" || r.Quote != orig || r.Forward {
		t.Errorf("reply = %+v", r)
	}
	if got := strings.Join(r.References, " "); got != "orig-0@example.com orig-1@example.com orig-2@example.com" {
		t.Errorf("References = %q", got)
	}
	if r.Text != "" || r.HTML != "" || r.Markdown != "" {
		t.Errorf("body fields should be empty")
	}

	// Reply-all drops self (any case) and anyone already in To.
	r = Reply(orig, self, true)
	if got := model.FormatAddresses(r.Cc); got != "dave@example.org, Carol <carol@example.com>" {
		t.Errorf("reply-all Cc = %q", got)
	}

	// Reply-To wins over From.
	orig.ReplyTo = []model.Address{{Email: "list@example.com"}}
	r = Reply(orig, self, true)
	if got := model.FormatAddresses(r.To); got != "list@example.com" {
		t.Errorf("Reply-To: To = %q", got)
	}
	if got := model.FormatAddresses(r.Cc); got != "dave@example.org, Carol <carol@example.com>" {
		t.Errorf("Reply-To: Cc = %q", got)
	}

	// Replying to your own sent message goes to its recipients.
	sent := &model.Message{MessageSummary: model.MessageSummary{
		MessageID: "<mine@example.com>",
		From:      []model.Address{{Email: "alice@example.com"}},
		To:        []model.Address{bob, {Email: "ALICE@EXAMPLE.COM"}},
		Cc:        []model.Address{carol, bob},
		Subject:   "RE: status",
	}}
	r = Reply(sent, self, false)
	if got := model.FormatAddresses(r.To); got != "Bob Smith <bob@example.com>" {
		t.Errorf("own message: To = %q", got)
	}
	if r.Subject != "RE: status" {
		t.Errorf("existing Re: prefix changed: %q", r.Subject)
	}
	if r.InReplyTo != "mine@example.com" || strings.Join(r.References, ",") != "mine@example.com" {
		t.Errorf("own message threading = %q %v", r.InReplyTo, r.References)
	}
	r = Reply(sent, self, true)
	if got := model.FormatAddresses(r.Cc); got != "Carol <carol@example.com>" {
		t.Errorf("own message reply-all Cc = %q", got)
	}

	// No References: the parent's In-Reply-To stands in.
	r = Reply(&model.Message{MessageSummary: model.MessageSummary{MessageID: "c@x", InReplyTo: "p@x", From: []model.Address{bob}}}, nil, false)
	if strings.Join(r.References, " ") != "p@x c@x" {
		t.Errorf("References = %v", r.References)
	}
}

func TestSubjectPrefixes(t *testing.T) {
	cases := []struct{ in, re, fwd string }{
		{"Hello", "Re: Hello", "Fwd: Hello"},
		{"re: Hello", "re: Hello", "Fwd: re: Hello"},
		{"  RE:Hello ", "RE:Hello", "Fwd: RE:Hello"},
		{"Fwd: Hello", "Re: Fwd: Hello", "Fwd: Hello"},
		{"FW: Hello", "Re: FW: Hello", "FW: Hello"},
		{"Reply needed", "Re: Reply needed", "Fwd: Reply needed"},
		{"", "Re:", "Fwd:"},
	}
	for _, c := range cases {
		m := &model.Message{MessageSummary: model.MessageSummary{Subject: c.in, From: []model.Address{bob}}}
		if got := Reply(m, nil, false).Subject; got != c.re {
			t.Errorf("Reply(%q).Subject = %q, want %q", c.in, got, c.re)
		}
		if got := Forward(m, false).Subject; got != c.fwd {
			t.Errorf("Forward(%q).Subject = %q, want %q", c.in, got, c.fwd)
		}
	}
}

func TestForwardAttachments(t *testing.T) {
	orig := origMessage()
	orig.HTML = `<p>logo <img src="cid:logo@x"></p>`
	orig.Attachments = []model.Attachment{
		{Index: 1, Filename: "logo.png", ContentType: "image/png", ContentID: "logo@x", Inline: true, Data: []byte("png")},
		{Index: 2, Filename: "report.pdf", ContentType: "application/pdf", Data: []byte("%PDF")},
	}

	f := Forward(orig, true)
	if !f.Forward || f.Quote != orig || f.Subject != "Fwd: Lunch plans" {
		t.Errorf("Forward = %+v", f)
	}
	if len(f.Attachments) != 2 || f.Attachments[0].ContentID != "logo@x" || !f.Attachments[0].Inline || f.Attachments[1].Filename != "report.pdf" {
		t.Fatalf("attachments = %+v", f.Attachments)
	}
	f.From, f.To = alice, []model.Address{dave}
	b := mustBuild(t, f)
	if b.Structure != "multipart/mixed(multipart/alternative(text/plain,multipart/related(text/html,image/png)),application/pdf)" {
		t.Errorf("Structure = %q", b.Structure)
	}
	if !strings.Contains(b.HTML, `<img src="cid:logo@x">`) {
		t.Errorf("quoted HTML lost the cid reference")
	}

	// Without attachments the inline image still comes along.
	f = Forward(orig, false)
	if len(f.Attachments) != 1 || f.Attachments[0].Filename != "logo.png" {
		t.Errorf("attachments without includeAttachments = %+v", f.Attachments)
	}
}
