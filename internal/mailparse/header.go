package mailparse

import (
	"bufio"
	"bytes"
	"mime"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-message/textproto"

	"gomail/internal/model"
)

// rawField is one header field as it appeared in the message.
type rawField struct {
	name  string // field name with its original capitalisation
	value string // unfolded value, not yet decoded
	raw   []byte // the field's bytes, including continuation lines
}

// splitHeader separates a header block from the body. It tolerates LF-only
// line endings, a leading UTF-8 BOM or mbox "From " line, continuation lines
// before the first field, and a missing blank line between header and body
// (the first line that is not a header field starts the body).
func splitHeader(data []byte) (fields []rawField, body []byte) {
	pos := 0
	if bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) {
		pos = 3
	}
	if bytes.HasPrefix(data[pos:], []byte("From ")) {
		if i := bytes.IndexByte(data[pos:], '\n'); i >= 0 {
			pos += i + 1
		} else {
			return nil, nil
		}
	}
	fieldStart := -1
	finish := func(end int) {
		if fieldStart >= 0 {
			fields = append(fields, makeField(data[fieldStart:end]))
			fieldStart = -1
		}
	}
	for pos < len(data) {
		next := len(data)
		end := len(data)
		if i := bytes.IndexByte(data[pos:], '\n'); i >= 0 {
			end = pos + i
			next = end + 1
		}
		line := bytes.TrimSuffix(data[pos:end], []byte("\r"))
		switch {
		case len(line) == 0:
			finish(pos)
			return fields, data[next:]
		case line[0] == ' ' || line[0] == '\t':
			// Continuation of the current field; a stray one before the
			// first field is dropped.
		case isHeaderLine(line):
			finish(pos)
			fieldStart = pos
		default:
			finish(pos)
			return fields, data[pos:]
		}
		pos = next
	}
	finish(len(data))
	return fields, nil
}

func isHeaderLine(line []byte) bool {
	colon := bytes.IndexByte(line, ':')
	if colon <= 0 {
		return false
	}
	key := bytes.TrimRight(line[:colon], " \t")
	if len(key) == 0 {
		return false
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func makeField(raw []byte) rawField {
	colon := bytes.IndexByte(raw, ':')
	return rawField{
		name:  string(bytes.TrimRight(raw[:colon], " \t")),
		value: unfold(raw[colon+1:]),
		raw:   raw,
	}
}

// unfold joins continuation lines with single spaces, like go-message does.
func unfold(v []byte) string {
	var b strings.Builder
	for _, line := range bytes.Split(v, []byte("\n")) {
		line = bytes.Trim(line, " \t\r")
		if len(line) == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.Write(line)
	}
	return b.String()
}

// buildHeader turns validated fields into a go-message header so its typed
// accessors (addresses, dates, message IDs) can be used.
func buildHeader(fields []rawField) mail.Header {
	var buf bytes.Buffer
	for _, f := range fields {
		buf.Write(f.raw)
		if !bytes.HasSuffix(f.raw, []byte("\n")) {
			buf.WriteString("\r\n")
		}
	}
	buf.WriteString("\r\n")
	h, _ := textproto.ReadHeader(bufio.NewReader(&buf))
	return mail.Header{Header: message.Header{Header: h}}
}

// ---- addresses ----

// parseAddresses reads an address list header. go-message's parser is tried
// first; if the field is malformed (8-bit names, missing quotes, stray
// separators) a lenient splitter recovers what it can. It never fails.
func parseAddresses(h mail.Header, key, hint string) []model.Address {
	raw := h.Get(key)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	if list, err := h.AddressList(key); err == nil {
		return convertAddresses(list, hint)
	}
	clean := decodeHeaderText8bit(raw, hint)
	if list, err := mail.ParseAddressList(clean); err == nil {
		return convertAddresses(list, hint)
	}
	return lenientAddressList(clean, hint)
}

// decodeHeaderText8bit only repairs non-UTF-8 bytes, leaving encoded-words
// in place for the address parser.
func decodeHeaderText8bit(v, hint string) string {
	if utf8.ValidString(v) {
		return v
	}
	if normalizeCharset(hint) == "" || isUTF8Label(normalizeCharset(hint)) {
		hint = "windows-1252"
	}
	return decodeCharset([]byte(v), hint)
}

func convertAddresses(list []*mail.Address, hint string) []model.Address {
	var out []model.Address
	for _, a := range list {
		if a == nil {
			continue
		}
		name := cleanName(a.Name, hint)
		email := strings.TrimSpace(a.Address)
		if name == "" && email == "" {
			continue
		}
		out = append(out, model.Address{Name: name, Email: email})
	}
	return out
}

// cleanName decodes encoded-words left in a display name (net/mail does not
// decode them inside quoted strings, which Outlook and others produce) and
// normalises whitespace.
func cleanName(name, hint string) string {
	name = decodeHeaderText(name, hint)
	name = strings.Join(strings.Fields(name), " ")
	if len(name) >= 2 && name[0] == '\'' && name[len(name)-1] == '\'' {
		name = name[1 : len(name)-1] // Outlook's 'Name' style
	}
	// Strip surrounding double quotes, including an unbalanced one.
	return strings.TrimSpace(strings.Trim(name, `"`))
}

// lenientAddressList splits an address list by hand: commas and semicolons
// outside quotes, angle brackets and comments separate entries; "group:"
// prefixes are dropped; "Name <addr>" and "addr (Name)" forms are understood.
// Entries without an address (like "undisclosed-recipients:;") are skipped.
func lenientAddressList(s, hint string) []model.Address {
	var out []model.Address
	for _, item := range splitAddressItems(s) {
		item = strings.TrimSpace(item)
		if i := groupColon(item); i >= 0 {
			item = strings.TrimSpace(item[i+1:])
		}
		if item == "" {
			continue
		}
		var name, email string
		if lt := strings.LastIndex(item, "<"); lt >= 0 {
			rest := item[lt+1:]
			if gt := strings.IndexByte(rest, '>'); gt >= 0 {
				email = rest[:gt]
			} else {
				email = rest
			}
			name = item[:lt]
		} else {
			name, email = splitComment(item)
		}
		email = strings.Trim(strings.TrimSpace(email), `<>"' `)
		name = cleanName(strings.TrimSpace(name), hint)
		if !strings.Contains(email, "@") {
			if email == "" || name == "" {
				continue
			}
		}
		out = append(out, model.Address{Name: name, Email: email})
	}
	return out
}

func splitAddressItems(s string) []string {
	items, balanced := splitAddressItemsQuoted(s, true)
	if !balanced {
		// An unterminated quote would swallow the rest of the list.
		items, _ = splitAddressItemsQuoted(s, false)
	}
	return items
}

func splitAddressItemsQuoted(s string, quotes bool) ([]string, bool) {
	var items []string
	var cur strings.Builder
	inQuote, angle, paren := false, 0, 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case !quotes && c == '"':
		case inQuote:
			if c == '\\' && i+1 < len(s) {
				cur.WriteByte(c)
				i++
				c = s[i]
			} else if c == '"' {
				inQuote = false
			}
		case c == '"':
			inQuote = true
		case c == '<':
			angle++
		case c == '>' && angle > 0:
			angle--
		case c == '(':
			paren++
		case c == ')' && paren > 0:
			paren--
		case (c == ',' || c == ';') && angle == 0 && paren == 0:
			items = append(items, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	items = append(items, cur.String())
	return items, !inQuote
}

// groupColon returns the index of a group-syntax colon ("Team: a@b") that
// precedes any address, or -1.
func groupColon(item string) int {
	inQuote := false
	for i := 0; i < len(item); i++ {
		switch c := item[i]; {
		case c == '"':
			inQuote = !inQuote
		case inQuote:
		case c == '<' || c == '@':
			return -1
		case c == ':':
			return i
		}
	}
	return -1
}

// splitComment splits "addr (Name)" into name and address.
func splitComment(item string) (name, email string) {
	open := strings.IndexByte(item, '(')
	if open < 0 {
		return "", item
	}
	end := strings.LastIndexByte(item, ')')
	if end < open {
		end = len(item)
	}
	name = item[open+1 : end]
	email = item[:open]
	if end < len(item) {
		email += item[end+1:]
	}
	return name, email
}

// ---- dates ----

var lenientDateLayouts = []string{
	"2 Jan 2006 15:04:05 -0700",
	"2 Jan 2006 15:04:05 MST",
	"2 Jan 2006 15:04:05 -07:00",
	"2 Jan 2006 15:04 -0700",
	"2 Jan 2006 15:04 MST",
	"2 Jan 06 15:04:05 -0700",
	"2 Jan 06 15:04:05 MST",
	"2 January 2006 15:04:05 -0700",
	"2 January 2006 15:04:05 MST",
	"2 Jan 2006 15:04:05",
	"2 Jan 2006 15:04",
	"Jan 2 15:04:05 2006",
	"Jan 2 15:04:05 MST 2006",
	"Jan 2 15:04:05 -0700 2006",
	"Jan 2 2006 15:04:05 -0700",
	"Jan 2, 2006 15:04:05 -0700",
	"Jan 2, 2006 3:04:05 PM -0700",
	"Jan 2, 2006 3:04 PM",
	"January 2, 2006 15:04:05 -0700",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05 -0700",
	"2006-01-02 15:04:05 -07:00",
	"2006-01-02 15:04:05 MST",
	"2006-01-02 15:04:05",
	"02.01.2006 15:04:05 -0700",
	"2.1.2006 15:04:05",
}

var weekdays = []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}

// parseDate reads the Date header with go-message and falls back to a set of
// lenient layouts. It returns the zero time if nothing matches.
func parseDate(h mail.Header) time.Time {
	if t, err := h.Date(); err == nil {
		return t
	}
	return lenientDate(h.Get("Date"))
}

func lenientDate(v string) time.Time {
	s := stripComments(v)
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, ",", ", ")), " ")
	// Drop a leading day-of-week ("Mon,", "Tuesday", "Thu.").
	if f := strings.Fields(s); len(f) > 1 {
		w := strings.ToLower(strings.TrimRight(f[0], ",."))
		for _, day := range weekdays {
			if len(w) >= 3 && strings.HasPrefix(day, w) {
				s = strings.Join(f[1:], " ")
				break
			}
		}
	}
	s = strings.TrimSpace(s)
	candidates := []string{s}
	// "-0700 PDT" or "+0000 GMT": keep only the numeric offset.
	if f := strings.Fields(s); len(f) > 2 {
		last := f[len(f)-1]
		prev := f[len(f)-2]
		if isAlpha(last) && (strings.HasPrefix(prev, "+") || strings.HasPrefix(prev, "-")) {
			candidates = append(candidates, strings.Join(f[:len(f)-1], " "))
		}
	}
	// Commas inside the date proper ("Jan 2, 2006") are kept above; also try
	// without any commas.
	candidates = append(candidates, strings.ReplaceAll(s, ",", ""))
	for _, c := range candidates {
		for _, layout := range lenientDateLayouts {
			if t, err := time.Parse(layout, c); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

func isAlpha(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return s != ""
}

// stripComments removes RFC 5322 parenthesised comments.
func stripComments(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '(':
			depth++
		case r == ')' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ---- message IDs ----

var msgIDPattern = regexp.MustCompile(`<([^<>\s]+)>`)

// lenientMsgIDs extracts message IDs from a header value: anything in angle
// brackets, or else bare tokens that look like addr-specs.
func lenientMsgIDs(v string) []string {
	var ids []string
	for _, m := range msgIDPattern.FindAllStringSubmatch(v, -1) {
		ids = append(ids, m[1])
	}
	if len(ids) > 0 {
		return ids
	}
	fields := strings.FieldsFunc(v, func(r rune) bool { return unicode.IsSpace(r) || r == ',' })
	for _, f := range fields {
		f = strings.Trim(f, "<>\"'")
		if strings.Contains(f, "@") {
			ids = append(ids, f)
		}
	}
	if len(ids) == 0 && len(fields) == 1 {
		if f := strings.Trim(fields[0], "<>\"'"); f != "" {
			ids = append(ids, f)
		}
	}
	return ids
}

func parseMessageID(h mail.Header) string {
	if id, err := h.MessageID(); err == nil && id != "" {
		return id
	}
	if ids := lenientMsgIDs(h.Get("Message-Id")); len(ids) > 0 {
		return ids[0]
	}
	return ""
}

func parseMsgIDList(h mail.Header, key string) []string {
	if ids, err := h.MsgIDList(key); err == nil {
		return ids
	}
	return lenientMsgIDs(h.Get(key))
}

// ---- MIME parameters ----

// parseParamHeader parses "value; key=val; ..." headers (Content-Type,
// Content-Disposition). mime.ParseMediaType handles the standard cases
// including RFC 2231; a lenient parser fills in anything it rejects (unquoted
// specials, duplicate keys, RFC 2231 charsets other than UTF-8). RFC 2047
// encoded-words and raw 8-bit bytes in values are decoded.
func parseParamHeader(v, hint string) (string, map[string]string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", map[string]string{}
	}
	mt, params, err := mime.ParseMediaType(v)
	lmt, lparams := lenientParams(v)
	if err != nil && mt == "" {
		mt = lmt
	}
	if params == nil {
		params = map[string]string{}
	}
	for k, val := range lparams {
		if _, ok := params[k]; !ok {
			params[k] = val
		}
	}
	for k, val := range params {
		if k == "boundary" {
			continue
		}
		params[k] = decodeHeaderText(val, hint)
	}
	return strings.ToLower(strings.TrimSpace(mt)), params
}

func lenientParams(v string) (string, map[string]string) {
	parts := splitParams(v)
	mt := strings.ToLower(strings.TrimSpace(parts[0]))
	raw := map[string]string{}
	var order []string
	for _, p := range parts[1:] {
		k, val, ok := strings.Cut(p, "=")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		if _, dup := raw[k]; dup {
			continue
		}
		raw[k] = unquote(strings.TrimSpace(val))
		order = append(order, k)
	}
	params := map[string]string{}
	for _, k := range order {
		if !strings.Contains(k, "*") {
			params[k] = raw[k]
		}
	}
	// RFC 2231 extended and continued parameters.
	seen := map[string]bool{}
	for _, k := range order {
		star := strings.IndexByte(k, '*')
		if star < 0 {
			continue
		}
		base := k[:star]
		if seen[base] {
			continue
		}
		seen[base] = true
		if val, ok := raw[base+"*"]; ok {
			cs, data := split2231(val)
			params[base] = decodeCharset(percentDecode(data), cs)
			continue
		}
		var buf []byte
		cs := ""
		for n := 0; ; n++ {
			idx := strconv.Itoa(n)
			if val, ok := raw[base+"*"+idx+"*"]; ok {
				if n == 0 {
					cs, val = split2231(val)
				}
				buf = append(buf, percentDecode(val)...)
			} else if val, ok := raw[base+"*"+idx]; ok {
				buf = append(buf, val...)
			} else {
				break
			}
		}
		if len(buf) > 0 {
			params[base] = decodeCharset(buf, cs)
		}
	}
	return mt, params
}

// split2231 splits "charset'lang'value". Without the quotes the whole string
// is the value.
func split2231(v string) (cs, value string) {
	first := strings.IndexByte(v, '\'')
	if first < 0 {
		return "", v
	}
	second := strings.IndexByte(v[first+1:], '\'')
	if second < 0 {
		return "", v
	}
	return v[:first], v[first+1+second+1:]
}

func percentDecode(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi, ok1 := unhex(s[i+1])
			lo, ok2 := unhex(s[i+2])
			if ok1 && ok2 {
				out = append(out, hi<<4|lo)
				i += 2
				continue
			}
		}
		out = append(out, s[i])
	}
	return out
}

func splitParams(v string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case inQuote && c == '\\' && i+1 < len(v):
			cur.WriteByte(c)
			i++
			c = v[i]
		case c == '"':
			inQuote = !inQuote
		case c == ';' && !inQuote:
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	return append(parts, cur.String())
}

func unquote(v string) string {
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		v = v[1 : len(v)-1]
		var b strings.Builder
		for i := 0; i < len(v); i++ {
			if v[i] == '\\' && i+1 < len(v) {
				i++
			}
			b.WriteByte(v[i])
		}
		return b.String()
	}
	if len(v) >= 1 && v[0] == '"' { // unterminated quote
		return v[1:]
	}
	return v
}
