package compose

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	xhtml "golang.org/x/net/html"

	"gomail/internal/htmltext"
	"gomail/internal/model"
)

// body is the final content of the message's text parts.
type body struct {
	text string
	html string // complete HTML document; "" for a text-only message
}

const (
	htmlDocStart = `<!DOCTYPE html><html><head><meta charset="utf-8"></head><body>`
	htmlDocEnd   = `</body></html>`

	blockquoteStyle = `margin:0px 0px 0px 0.8ex;border-left:1px solid rgb(204,204,204);padding-left:1ex`

	forwardSeparator = "---------- Forwarded message ---------"

	// attributionDateLayout matches Gmail's quote attribution:
	// "On Fri, 2 Oct 2026 at 11:15, Name <email> wrote:".
	attributionDateLayout = "Mon, 2 Jan 2006 at 15:04"
)

var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(
		gmhtml.WithHardWraps(),
		// The author is the sender, so inline HTML (e.g. <img src="cid:...">)
		// is passed through rather than dropped.
		gmhtml.WithUnsafe(),
	),
)

// RenderMarkdown renders GitHub-flavoured Markdown (tables, strikethrough,
// autolinks, task lists, hard line breaks) to an HTML fragment.
func RenderMarkdown(src string) (string, error) {
	var buf bytes.Buffer
	if err := markdown.Convert([]byte(src), &buf); err != nil {
		return "", fmt.Errorf("compose: rendering Markdown: %w", err)
	}
	return buf.String(), nil
}

func blank(s string) bool { return strings.TrimSpace(s) == "" }

// composeBody works out the final text and HTML parts: the body itself, then
// the signature, then the reply quote or forwarded message.
func composeBody(o *Options) (body, error) {
	hasMD, hasText, hasHTML := !blank(o.Markdown), !blank(o.Text), !blank(o.HTML)
	if hasMD && (hasText || hasHTML) {
		return body{}, errors.New("compose: a Markdown body can't be combined with a Text or HTML body")
	}

	var text, frag string
	wantHTML := false
	switch {
	case hasMD:
		rendered, err := RenderMarkdown(o.Markdown)
		if err != nil {
			return body{}, err
		}
		text, frag, wantHTML = o.Markdown, rendered, true
	case hasHTML && hasText:
		text, frag, wantHTML = o.Text, o.HTML, true
	case hasHTML:
		text, frag, wantHTML = htmltext.Convert(o.HTML), o.HTML, true
	case hasText:
		text = o.Text
		// Forwarding an HTML message: send HTML too so the forwarded
		// content keeps its formatting, as Gmail does.
		if o.Forward && o.Quote != nil && !blank(o.Quote.HTML) {
			frag, wantHTML = TextToHTML(o.Text), true
		}
	default:
		if o.Quote == nil && len(o.Attachments) == 0 {
			return body{}, errors.New("compose: the message has no body (give Text, HTML or Markdown), no attachments and nothing quoted")
		}
		// No body of our own: follow the format of the quoted message so a
		// bare forward keeps the original's HTML.
		wantHTML = o.Quote != nil && !blank(o.Quote.HTML)
	}

	sigText, sigHTML := signatureParts(o.Signature)

	// Text part.
	text = trimRightSpace(normalizeNewlines(text))
	text = joinBlocks(text, sigText)
	if q := o.Quote; q != nil {
		if o.Forward {
			text = joinBlocks(text, forwardText(q))
		} else {
			text = joinBlocks(text, replyText(q))
		}
	}
	if text != "" {
		text += "\n"
	}

	out := body{text: text}
	if !wantHTML {
		return out, nil
	}

	// HTML part.
	var after strings.Builder // appended after the body, inside the wrapper
	if sigHTML != "" {
		after.WriteString(`<br><div class="gmail_signature">`)
		after.WriteString(sigHTML)
		after.WriteString(`</div>`)
	}
	quote := ""
	if q := o.Quote; q != nil {
		if o.Forward {
			quote = `<br>` + forwardHTML(q)
		} else {
			quote = `<br>` + replyHTML(q)
		}
	}

	if isFullDocument(frag) {
		out.html = insertBeforeBodyEnd(frag, after.String()+quote)
	} else {
		out.html = htmlDocStart + `<div dir="ltr">` + strings.TrimSpace(frag) + after.String() + `</div>` + quote + htmlDocEnd
	}
	return out, nil
}

// signatureParts returns the signature's text and HTML, deriving whichever
// one is missing from the other.
func signatureParts(sig *Signature) (text, htm string) {
	if sig == nil {
		return "", ""
	}
	text = strings.Trim(normalizeNewlines(sig.Text), "\n")
	text = trimRightSpace(text)
	htm = strings.TrimSpace(sig.HTML)
	switch {
	case text == "" && htm != "":
		text = trimRightSpace(strings.Trim(normalizeNewlines(htmltext.Convert(htm)), "\n"))
	case htm == "" && text != "":
		htm = TextToHTML(text)
	}
	return text, htm
}

// replyText renders Gmail's plain-text reply quote: an attribution line and
// the original text with every line prefixed by "> ".
func replyText(q *model.Message) string {
	var b strings.Builder
	b.WriteString(attributionText(q))
	b.WriteString("\n")
	orig := trimRightSpace(normalizeNewlines(quotedText(q)))
	for i, line := range strings.Split(orig, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		switch {
		case line == "":
			b.WriteString(">")
		case strings.HasPrefix(line, ">"):
			b.WriteString(">" + line)
		default:
			b.WriteString("> " + line)
		}
	}
	return b.String()
}

// forwardText renders Gmail's plain-text forwarded-message block.
func forwardText(q *model.Message) string {
	var b strings.Builder
	b.WriteString(forwardSeparator + "\n")
	b.WriteString("From: " + attributionAddrs(q.From) + "\n")
	if !q.Date.IsZero() {
		b.WriteString("Date: " + attributionDate(q.Date) + "\n")
	}
	b.WriteString("Subject: " + oneLine(q.Subject) + "\n")
	if len(q.To) > 0 {
		b.WriteString("To: " + attributionAddrs(q.To) + "\n")
	}
	if len(q.Cc) > 0 {
		b.WriteString("Cc: " + attributionAddrs(q.Cc) + "\n")
	}
	if orig := trimRightSpace(normalizeNewlines(quotedText(q))); orig != "" {
		b.WriteString("\n" + orig)
	}
	return trimRightSpace(b.String())
}

// replyHTML renders Gmail's HTML reply quote.
func replyHTML(q *model.Message) string {
	var b strings.Builder
	b.WriteString(`<div class="gmail_quote"><div dir="ltr" class="gmail_attr">`)
	b.WriteString(attributionHTML(q))
	b.WriteString(`<br></div><blockquote class="gmail_quote" style="` + blockquoteStyle + `">`)
	b.WriteString(quotedHTML(q))
	b.WriteString(`</blockquote></div>`)
	return b.String()
}

// forwardHTML renders Gmail's HTML forwarded-message block.
func forwardHTML(q *model.Message) string {
	var b strings.Builder
	b.WriteString(`<div class="gmail_quote"><div dir="ltr" class="gmail_attr">` + forwardSeparator + `<br>`)
	b.WriteString("From: ")
	for i, a := range q.From {
		if i > 0 {
			b.WriteString(", ")
		}
		if a.Name != "" {
			b.WriteString(`<strong class="gmail_sendername" dir="auto">` + html.EscapeString(a.Name) + `</strong> `)
		}
		b.WriteString(`<span dir="auto">` + mailtoHTML(a.Email) + `</span>`)
	}
	b.WriteString("<br>")
	if !q.Date.IsZero() {
		b.WriteString("Date: " + html.EscapeString(attributionDate(q.Date)) + "<br>")
	}
	b.WriteString("Subject: " + html.EscapeString(oneLine(q.Subject)) + "<br>")
	if len(q.To) > 0 {
		b.WriteString("To: " + attributionAddrsHTML(q.To) + "<br>")
	}
	if len(q.Cc) > 0 {
		b.WriteString("Cc: " + attributionAddrsHTML(q.Cc) + "<br>")
	}
	b.WriteString(`</div><br><br>`)
	b.WriteString(quotedHTML(q))
	b.WriteString(`</div>`)
	return b.String()
}

// quotedText is the original's plain text, derived from its HTML if needed.
func quotedText(q *model.Message) string {
	if !blank(q.Text) || blank(q.HTML) {
		return q.Text
	}
	return htmltext.Convert(q.HTML)
}

// quotedHTML is the original's HTML body content, or its text as HTML.
func quotedHTML(q *model.Message) string {
	if !blank(q.HTML) {
		return bodyContent(q.HTML)
	}
	return TextToHTML(trimRightSpace(normalizeNewlines(q.Text)))
}

func attributionText(q *model.Message) string {
	who := attributionAddrs(q.From)
	switch {
	case q.Date.IsZero() && who == "":
		return "Someone wrote:"
	case q.Date.IsZero():
		return who + " wrote:"
	case who == "":
		return "On " + attributionDate(q.Date) + ", someone wrote:"
	}
	return "On " + attributionDate(q.Date) + ", " + who + " wrote:"
}

func attributionHTML(q *model.Message) string {
	who := attributionAddrsHTML(q.From)
	date := html.EscapeString(attributionDate(q.Date))
	switch {
	case q.Date.IsZero() && who == "":
		return "Someone wrote:"
	case q.Date.IsZero():
		return who + " wrote:"
	case who == "":
		return "On " + date + ", someone wrote:"
	}
	return "On " + date + ", " + who + " wrote:"
}

func attributionDate(t time.Time) string {
	return t.In(time.Local).Format(attributionDateLayout)
}

// attributionAddrs formats addresses as Gmail does in quote headers:
// "Name <email>", or "<email>" when there's no name.
func attributionAddrs(list []model.Address) string {
	parts := make([]string, 0, len(list))
	for _, a := range list {
		name := strings.TrimSpace(oneLine(a.Name))
		if name == "" {
			parts = append(parts, "<"+a.Email+">")
		} else {
			parts = append(parts, name+" <"+a.Email+">")
		}
	}
	return strings.Join(parts, ", ")
}

func attributionAddrsHTML(list []model.Address) string {
	parts := make([]string, 0, len(list))
	for _, a := range list {
		name := strings.TrimSpace(oneLine(a.Name))
		if name == "" {
			parts = append(parts, mailtoHTML(a.Email))
		} else {
			parts = append(parts, html.EscapeString(name)+" "+mailtoHTML(a.Email))
		}
	}
	return strings.Join(parts, ", ")
}

// mailtoHTML renders "&lt;<a href="mailto:email">email</a>&gt;".
func mailtoHTML(email string) string {
	e := html.EscapeString(email)
	return `&lt;<a href="mailto:` + e + `">` + e + `</a>&gt;`
}

// linkPattern finds URLs and email addresses in plain text. It's a
// structural match used only to make links clickable.
var linkPattern = regexp.MustCompile(`(?i)\b(?:https?://|mailto:|www\.)[^\s<>"]+|\b[a-z0-9._%+\-]+@[a-z0-9\-]+(?:\.[a-z0-9\-]+)*\.[a-z]{2,}\b`)

// TextToHTML converts plain text to an HTML fragment: it escapes the text,
// turns newlines into <br>, and makes URLs and email addresses clickable.
func TextToHTML(text string) string {
	text = normalizeNewlines(text)
	var b strings.Builder
	last := 0
	for _, m := range linkPattern.FindAllStringIndex(text, -1) {
		start, end := m[0], m[1]
		// Leave trailing punctuation outside the link ("see https://x.com."),
		// but keep a closing parenthesis that balances one in the URL.
		for end > start {
			c := text[end-1]
			if c == ')' && strings.Count(text[start:end], "(") >= strings.Count(text[start:end], ")") {
				break
			}
			if !strings.ContainsRune(".,;:!?)]}'", rune(c)) {
				break
			}
			end--
		}
		if end <= start || start < last {
			continue
		}
		b.WriteString(escapeText(text[last:start]))
		link := text[start:end]
		href := link
		switch {
		case strings.HasPrefix(strings.ToLower(link), "www."):
			href = "http://" + link
		case !strings.Contains(link, "://") && !strings.HasPrefix(strings.ToLower(link), "mailto:"):
			href = "mailto:" + link
		}
		b.WriteString(`<a href="` + html.EscapeString(href) + `">` + html.EscapeString(link) + `</a>`)
		last = end
	}
	b.WriteString(escapeText(text[last:]))
	return b.String()
}

func escapeText(s string) string {
	return strings.ReplaceAll(html.EscapeString(s), "\n", "<br>")
}

// normalizeNewlines converts CRLF and lone CR line endings to LF.
func normalizeNewlines(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func trimRightSpace(s string) string {
	return strings.TrimRight(s, " \t\n")
}

// joinBlocks joins two blocks of text with a blank line between them.
func joinBlocks(a, b string) string {
	switch {
	case b == "":
		return a
	case a == "":
		return b
	}
	return a + "\n\n" + b
}

// htmlLayout locates the structural tags of an HTML string.
type htmlLayout struct {
	hasDocTags bool // has an <html> or <body> tag
	bodyStart  int  // offset just after <body ...>, -1 if absent
	bodyEnd    int  // offset of the last </body>, -1 if absent
	htmlEnd    int  // offset of the last </html>, -1 if absent
}

func scanHTML(s string) htmlLayout {
	l := htmlLayout{bodyStart: -1, bodyEnd: -1, htmlEnd: -1}
	z := xhtml.NewTokenizer(strings.NewReader(s))
	off := 0
	for {
		tt := z.Next()
		if tt == xhtml.ErrorToken {
			return l
		}
		raw := len(z.Raw())
		if tt == xhtml.StartTagToken || tt == xhtml.EndTagToken || tt == xhtml.SelfClosingTagToken {
			name, _ := z.TagName()
			switch string(name) {
			case "html":
				l.hasDocTags = true
				if tt == xhtml.EndTagToken {
					l.htmlEnd = off
				}
			case "body":
				l.hasDocTags = true
				if tt == xhtml.EndTagToken {
					l.bodyEnd = off
				} else if l.bodyStart < 0 {
					l.bodyStart = off + raw
				}
			}
		}
		off += raw
	}
}

// isFullDocument reports whether s is a complete HTML document rather than a
// fragment.
func isFullDocument(s string) bool {
	return scanHTML(s).hasDocTags
}

// insertBeforeBodyEnd inserts extra just before </body> (or </html>), or
// appends it when the document has neither.
func insertBeforeBodyEnd(doc, extra string) string {
	if extra == "" {
		return doc
	}
	l := scanHTML(doc)
	at := len(doc)
	switch {
	case l.bodyEnd >= 0:
		at = l.bodyEnd
	case l.htmlEnd >= 0:
		at = l.htmlEnd
	}
	return doc[:at] + extra + doc[at:]
}

// bodyContent returns what's inside <body> for a full document, or the
// fragment itself.
func bodyContent(s string) string {
	l := scanHTML(s)
	if l.bodyStart < 0 {
		return strings.TrimSpace(s)
	}
	end := len(s)
	switch {
	case l.bodyEnd >= l.bodyStart:
		end = l.bodyEnd
	case l.htmlEnd >= l.bodyStart:
		end = l.htmlEnd
	}
	return strings.TrimSpace(s[l.bodyStart:end])
}
