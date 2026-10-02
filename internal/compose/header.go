package compose

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"

	"gomail/internal/model"
)

// field is one header field in output order.
type field struct {
	k, v string
}

// foldField renders "Key: value" plus CRLF, folding at spaces so lines stay
// within 76 characters where possible. Folding only ever replaces an existing
// space with CRLF+space, so unfolding restores the value exactly.
func foldField(k, v string) []byte {
	var b strings.Builder
	b.Grow(len(k) + len(v) + 8)
	b.WriteString(k)
	b.WriteString(":")
	lineLen := len(k) + 1
	for i, w := range strings.Split(v, " ") {
		// Every word but the first can move to a continuation line; the
		// space that separated it starts that line.
		if i > 0 && lineLen+1+len(w) > 76 {
			b.WriteString("\r\n")
			lineLen = 0
		}
		b.WriteByte(' ')
		b.WriteString(w)
		lineLen += 1 + len(w)
	}
	b.WriteString("\r\n")
	return []byte(b.String())
}

// oneLine replaces line breaks and other control characters with spaces so a
// value can't inject header fields.
func oneLine(s string) string {
	if strings.IndexFunc(s, isCtl) < 0 {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isCtl(r) {
			return ' '
		}
		return r
	}, s)
}

func isCtl(r rune) bool { return r < ' ' || r == 0x7f }

// encodeText encodes an unstructured header value (RFC 2047) when it contains
// anything but printable ASCII.
func encodeText(s string) string {
	s = oneLine(s)
	if isPrintableASCII(s) {
		return s
	}
	return encodeWords(s)
}

// maxEncodedWord keeps encoded-words short enough that a folded header line
// holding one stays within 78 characters.
const maxEncodedWord = 64

// encodeWords encodes s as a run of RFC 2047 Q-encoded words separated by
// spaces (which decoders drop between adjacent encoded-words). It uses only
// the characters RFC 2047 allows in a phrase, so the result is also safe for
// display names and quoted parameters. Words never split a UTF-8 sequence.
func encodeWords(s string) string {
	const prefix, suffix = "=?utf-8?q?", "?="
	var out, word strings.Builder
	flush := func() {
		if word.Len() == 0 {
			return
		}
		if out.Len() > 0 {
			out.WriteByte(' ')
		}
		out.WriteString(prefix + word.String() + suffix)
		word.Reset()
	}
	for _, r := range s {
		enc := qEncodeRune(r)
		if word.Len()+len(enc) > maxEncodedWord-len(prefix)-len(suffix) {
			flush()
		}
		word.WriteString(enc)
	}
	flush()
	return out.String()
}

func qEncodeRune(r rune) string {
	switch {
	case r == ' ':
		return "_"
	case r < utf8.RuneSelf && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!*+-/", r)):
		return string(r)
	}
	const hexDigits = "0123456789ABCDEF"
	var buf [utf8.UTFMax]byte
	n := utf8.EncodeRune(buf[:], r)
	var b strings.Builder
	for _, c := range buf[:n] {
		b.WriteByte('=')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0xf])
	}
	return b.String()
}

// cleanAddress trims an address and checks the email is usable on the wire.
func cleanAddress(a model.Address) (model.Address, error) {
	a.Name = strings.TrimSpace(oneLine(a.Name))
	a.Email = strings.TrimSpace(a.Email)
	if a.Email == "" {
		if a.Name == "" {
			return a, nil
		}
		return a, fmt.Errorf("address %q has no email", a.Name)
	}
	if strings.ContainsAny(a.Email, "<>,; \t\r\n") || strings.IndexFunc(a.Email, isCtl) >= 0 {
		return a, fmt.Errorf("invalid email address %q", a.Email)
	}
	at := strings.LastIndexByte(a.Email, '@')
	if at <= 0 || at == len(a.Email)-1 {
		return a, fmt.Errorf("invalid email address %q", a.Email)
	}
	return a, nil
}

func cleanAddresses(list []model.Address) ([]model.Address, error) {
	var out []model.Address
	for _, a := range list {
		c, err := cleanAddress(a)
		if err != nil {
			return nil, err
		}
		if c.Email == "" {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// formatAddressList renders addresses for an address header, quoting or
// RFC 2047-encoding display names as needed.
func formatAddressList(list []model.Address) string {
	parts := make([]string, len(list))
	for i, a := range list {
		if isPrintableASCII(a.Name) {
			parts[i] = (&mail.Address{Name: a.Name, Address: a.Email}).String()
		} else {
			parts[i] = encodeWords(a.Name) + " " + (&mail.Address{Address: a.Email}).String()
		}
	}
	return strings.Join(parts, ", ")
}

func appendAddressField(fields []field, name string, list []model.Address) []field {
	if len(list) == 0 {
		return fields
	}
	return append(fields, field{name, formatAddressList(list)})
}

// cleanMsgID strips whitespace and angle brackets from a Message-ID.
func cleanMsgID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "<")
	id = strings.TrimSuffix(id, ">")
	id = strings.TrimSpace(id)
	if strings.ContainsAny(id, "<> \t\r\n") {
		return ""
	}
	return id
}

// cleanMsgIDs cleans and dedupes a list of Message-IDs, dropping empty ones.
func cleanMsgIDs(ids []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, id := range ids {
		// Tolerate a single string holding several "<a> <b>" IDs.
		for _, part := range strings.Fields(strings.NewReplacer("<", " ", ">", " ").Replace(id)) {
			part = cleanMsgID(part)
			if part == "" || seen[part] {
				continue
			}
			seen[part] = true
			out = append(out, part)
		}
	}
	return out
}

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// generateMessageID returns a random Message-ID (without <>) whose right-hand
// side is the sender's domain, or gomail.local when there isn't a usable one.
func generateMessageID(fromEmail string) (string, error) {
	var b [20]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("compose: generating Message-ID: %w", err)
	}
	return strings.ToLower(idEncoding.EncodeToString(b[:])) + "@" + messageIDDomain(fromEmail), nil
}

func messageIDDomain(email string) string {
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return "gomail.local"
	}
	domain := strings.ToLower(strings.TrimSpace(email[at+1:]))
	if domain == "" || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "gomail.local"
	}
	for _, r := range domain {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
			return "gomail.local"
		}
	}
	return domain
}

// reservedHeaders can't be set through Options.Headers because they're
// derived from other options (or define the MIME structure).
var reservedHeaders = map[string]string{
	"from":                      "Options.From",
	"to":                        "Options.To",
	"cc":                        "Options.Cc",
	"bcc":                       "Options.Bcc",
	"message-id":                "Options.MessageID",
	"mime-version":              "",
	"content-type":              "",
	"content-transfer-encoding": "",
	"content-disposition":       "",
	"content-id":                "",
}

// mergeExtraHeaders appends extra headers. The first extra header with the
// same name as a computed one (Subject, Reply-To, User-Agent, ...) replaces it;
// further ones are added alongside.
func mergeExtraHeaders(fields []field, extra []model.Header) ([]field, error) {
	replaced := map[string]bool{}
	for _, h := range extra {
		name := strings.TrimSpace(h.Name)
		if name == "" {
			return nil, fmt.Errorf("compose: extra header with an empty name")
		}
		for _, r := range name {
			if r <= ' ' || r > '~' || r == ':' {
				return nil, fmt.Errorf("compose: invalid header name %q", name)
			}
		}
		if strings.ContainsAny(h.Value, "\r\n") {
			return nil, fmt.Errorf("compose: header %s contains a line break", name)
		}
		lower := strings.ToLower(name)
		if opt, ok := reservedHeaders[lower]; ok {
			if opt != "" {
				return nil, fmt.Errorf("compose: header %s can't be set directly; use %s", name, opt)
			}
			return nil, fmt.Errorf("compose: header %s is generated and can't be set directly", name)
		}
		value := encodeText(strings.TrimSpace(h.Value))
		if !replaced[lower] {
			replaced[lower] = true
			kept := fields[:0]
			for _, f := range fields {
				if !strings.EqualFold(f.k, name) {
					kept = append(kept, f)
				}
			}
			fields = kept
		}
		fields = append(fields, field{name, value})
	}
	return fields, nil
}
