// Package mailparse turns raw RFC 5322 messages into model.Message values.
//
// Parsing is deliberately tolerant because real-world mail is messy: unknown
// charsets and transfer encodings, malformed address lists, broken or missing
// multipart boundaries and truncated parts never fail the parse. Parse only
// returns an error when the input has no readable header at all.
package mailparse

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"gomail/internal/htmltext"
	"gomail/internal/model"
)

// ErrNoHeader is returned by Parse when the input does not start with a
// message header.
var ErrNoHeader = errors.New("mailparse: no message header found")

// Parse parses a raw RFC 5322 message. Gmail/IMAP fields (ID, ThreadID,
// Labels, Flags, Unread, Starred, Mailbox, UID) are left for the caller.
func Parse(raw []byte) (*model.Message, error) {
	fields, body := splitHeader(raw)
	if len(fields) == 0 {
		return nil, ErrNoHeader
	}

	// Raw 8-bit header bytes are most likely in the charset of the body.
	hint := ""
	for _, f := range fields {
		if strings.EqualFold(f.name, "Content-Type") {
			_, params := parseParamHeader(f.value, "")
			hint = params["charset"]
			break
		}
	}

	root := parseEntity(fields, body, "text/plain", hint, 0)
	h := root.header

	m := &model.Message{Raw: raw}
	m.Size = uint32(len(raw))
	m.Headers = make([]model.Header, 0, len(fields))
	for _, f := range fields {
		m.Headers = append(m.Headers, model.Header{Name: f.name, Value: decodeHeaderText(f.value, hint)})
	}
	m.Subject = singleLine(decodeHeaderText(h.Get("Subject"), hint))
	m.From = parseAddresses(h, "From", hint)
	m.To = parseAddresses(h, "To", hint)
	m.Cc = parseAddresses(h, "Cc", hint)
	m.Bcc = parseAddresses(h, "Bcc", hint)
	m.ReplyTo = parseAddresses(h, "Reply-To", hint)
	m.Date = parseDate(h)
	m.MessageID = parseMessageID(h)
	if ids := parseMsgIDList(h, "In-Reply-To"); len(ids) > 0 {
		m.InReplyTo = ids[0]
	}
	m.References = parseMsgIDList(h, "References")

	// Body.
	sel := &selector{redundant: map[*node]bool{}}
	groups := sel.selectBody(root)
	used := map[*node]bool{}
	var texts, htmls []string
	for _, g := range groups {
		var pt, ht []string
		for _, n := range g.plain {
			used[n] = true
			pt = append(pt, leafText(n))
		}
		for _, n := range g.html {
			used[n] = true
			ht = append(ht, strings.TrimSpace(leafHTML(n)))
		}
		plain := joinSections(pt)
		htmlBody := strings.Join(nonBlank(ht), "\n")
		switch {
		case plain != "":
			texts = append(texts, plain)
		case htmlBody != "":
			texts = append(texts, htmltext.Convert(htmlBody))
			m.TextFromHTML = true
		}
		if htmlBody != "" {
			htmls = append(htmls, htmlBody)
		}
	}
	m.Text = joinSections(texts)
	m.HTML = strings.Join(htmls, "\n")

	// Attachments: every other leaf, in order of appearance.
	m.Attachments = []model.Attachment{}
	walkLeaves(root, func(n *node) {
		if used[n] || sel.redundant[n] {
			return
		}
		data := decodeTransfer(n.body, n.encoding, false)
		cid := n.contentID()
		a := model.Attachment{
			Index:       len(m.Attachments) + 1,
			ContentType: n.mediaType,
			Size:        len(data),
			ContentID:   cid,
			Inline:      n.disp == "inline" || (n.disp == "" && cid != ""),
			Data:        data,
		}
		a.Filename = attachmentName(n, a.Index, data)
		m.Attachments = append(m.Attachments, a)
		if !a.Inline {
			m.HasAttach = true
		}
	})
	return m, nil
}

func nonBlank(parts []string) []string {
	var out []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// singleLine replaces control characters (stray CR/LF/TAB from decoding)
// with spaces and trims.
func singleLine(s string) string {
	if strings.ContainsFunc(s, unicode.IsControl) {
		s = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, s)
	}
	return strings.TrimSpace(s)
}

// DecodeSnippet decodes a possibly-truncated body part fetched over IMAP and
// returns up to maxRunes of whitespace-collapsed plain text (maxRunes <= 0
// means no limit).
//
// The transfer encoding is undone tolerantly (a trailing partial base64
// quantum or quoted-printable escape is dropped), the charset converted,
// HTML rendered as text, and quoted-reply lines ("> ...") dropped when there
// is other content.
func DecodeSnippet(data []byte, encoding, mediaType, charset string, maxRunes int) string {
	b := decodeTransfer(data, encoding, true)
	mt := strings.ToLower(strings.TrimSpace(mediaType))
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = strings.TrimSpace(mt[:i])
	}
	var text string
	if mt == "text/html" || mt == "html" {
		h := decodeHTMLCharset(b, charset)
		// A tag cut off by truncation ("<", "</di") would otherwise be shown
		// as text by the HTML tokenizer.
		if lt := strings.LastIndexByte(h, '<'); lt >= 0 && !strings.Contains(h[lt:], ">") {
			h = h[:lt]
		}
		text = htmltext.Convert(h)
	} else {
		text = decodeCharset(b, charset)
	}
	text = strings.ReplaceAll(text, string(utf8.RuneError), "")
	text = dropQuoted(normalizeNewlines(text))
	text = strings.Join(strings.Fields(text), " ")
	if maxRunes > 0 && utf8.RuneCountInString(text) > maxRunes {
		i, n := 0, 0
		for i < len(text) && n < maxRunes {
			_, size := utf8.DecodeRuneInString(text[i:])
			i += size
			n++
		}
		text = strings.TrimRight(text[:i], " ")
	}
	return text
}

// dropQuoted removes quoted-reply lines when other text remains. If
// everything is quoted, the quote markers are stripped instead.
func dropQuoted(text string) string {
	lines := strings.Split(text, "\n")
	var kept []string
	for _, l := range lines {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, ">") {
			kept = append(kept, l)
		}
	}
	if len(kept) > 0 {
		return strings.Join(kept, "\n")
	}
	for i, l := range lines {
		lines[i] = strings.TrimLeft(strings.TrimSpace(l), "> ")
	}
	return strings.Join(lines, "\n")
}
