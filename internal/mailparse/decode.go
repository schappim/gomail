package mailparse

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-message/charset"
	htmlcharset "golang.org/x/net/html/charset"
	"golang.org/x/text/encoding/charmap"
)

// decodeTransfer undoes a Content-Transfer-Encoding. It never fails: junk in
// base64 is skipped, broken quoted-printable escapes are kept literally, and
// unknown encodings pass the bytes through. When truncated is set the input
// was cut off mid-stream, so a trailing partial base64 quantum or a partial
// "=X" escape is dropped instead of being decoded.
func decodeTransfer(data []byte, encoding string, truncated bool) []byte {
	switch normalizeEncoding(encoding) {
	case "base64":
		return decodeBase64(data, truncated)
	case "quoted-printable":
		return decodeQP(data, truncated)
	default: // 7bit, 8bit, binary, "", or unknown
		return data
	}
}

func normalizeEncoding(enc string) string {
	enc = strings.ToLower(strings.TrimSpace(enc))
	if i := strings.IndexAny(enc, "; \t("); i >= 0 {
		enc = enc[:i]
	}
	return strings.Trim(enc, `"'`)
}

func isBase64Char(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/'
}

// decodeBase64 decodes base64 tolerantly. Characters outside the alphabet are
// ignored and "=" padding ends a segment, so bodies that pad every line
// independently still decode.
func decodeBase64(data []byte, truncated bool) []byte {
	out := make([]byte, 0, len(data)*3/4+3)
	seg := make([]byte, 0, 1024)
	flush := func(final bool) {
		n := len(seg)
		if final && truncated {
			n -= n % 4
		} else if n%4 == 1 {
			n-- // a single leftover character carries no full byte
		}
		if n > 0 {
			buf := make([]byte, base64.RawStdEncoding.DecodedLen(n))
			w, _ := base64.RawStdEncoding.Decode(buf, seg[:n])
			out = append(out, buf[:w]...)
		}
		seg = seg[:0]
	}
	for _, c := range data {
		switch {
		case isBase64Char(c):
			seg = append(seg, c)
		case c == '=':
			if len(seg) > 0 {
				flush(false)
			}
		}
	}
	flush(true)
	return out
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}

// decodeQP decodes quoted-printable leniently: lowercase hex is accepted,
// invalid escapes are kept literally, soft line breaks may be followed by
// stray whitespace, and transport-added trailing whitespace is removed.
func decodeQP(data []byte, truncated bool) []byte {
	out := make([]byte, 0, len(data))
	n := len(data)
	for i := 0; i < n; {
		c := data[i]
		switch c {
		case '=':
			// Soft line break: "=" + optional whitespace + (CR)LF.
			j := i + 1
			for j < n && (data[j] == ' ' || data[j] == '\t') {
				j++
			}
			if j < n && data[j] == '\n' {
				i = j + 1
				continue
			}
			if j+1 < n && data[j] == '\r' && data[j+1] == '\n' {
				i = j + 2
				continue
			}
			if j == n || (j == n-1 && data[j] == '\r') {
				i = n // "=" at the very end: a soft break with nothing after it
				continue
			}
			if i+2 < n {
				hi, ok1 := unhex(data[i+1])
				lo, ok2 := unhex(data[i+2])
				if ok1 && ok2 {
					out = append(out, hi<<4|lo)
					i += 3
					continue
				}
			}
			if truncated && i+2 == n {
				if _, ok := unhex(data[i+1]); ok {
					i = n // partial "=X" escape cut off by truncation
					continue
				}
			}
			out = append(out, '=')
			i++
		case ' ', '\t':
			j := i
			for j < n && (data[j] == ' ' || data[j] == '\t') {
				j++
			}
			if j == n || data[j] == '\n' || (data[j] == '\r' && (j+1 == n || data[j+1] == '\n')) {
				i = j // trailing whitespace on a line
				continue
			}
			out = append(out, data[i:j]...)
			i = j
		default:
			out = append(out, c)
			i++
		}
	}
	return out
}

// normalizeCharset lowercases a charset label and strips quotes and an RFC
// 2231 language suffix ("utf-8*en").
func normalizeCharset(cs string) string {
	cs = strings.ToLower(strings.TrimSpace(cs))
	cs = strings.Trim(cs, `"' `)
	if i := strings.IndexByte(cs, '*'); i >= 0 {
		cs = cs[:i]
	}
	return cs
}

func isUTF8Label(cs string) bool {
	switch cs {
	case "utf-8", "utf8", "unicode-1-1-utf-8", "x-unicode20utf8":
		return true
	}
	return false
}

func isASCIILabel(cs string) bool {
	switch cs {
	case "", "us-ascii", "ascii", "us", "ansi_x3.4-1968", "iso646-us", "cp367", "ibm367", "csascii", "default", "unknown", "x-unknown", "unknown-8bit":
		return true
	}
	return false
}

// isLatin1Label reports labels decoded as Windows-1252, a superset of
// ISO-8859-1 that also covers the "smart quotes" mislabelled senders put in
// the 0x80-0x9F range (the WHATWG encoding standard does the same).
func isLatin1Label(cs string) bool {
	switch cs {
	case "iso-8859-1", "iso8859-1", "iso_8859-1", "iso_8859-1:1987", "latin1", "latin-1", "l1",
		"cp819", "ibm819", "iso-ir-100", "csisolatin1", "windows-1252", "cp1252", "x-cp1252", "cp-1252", "win-1252":
		return true
	}
	return false
}

func win1252(data []byte) string {
	s, err := charmap.Windows1252.NewDecoder().Bytes(data)
	if err != nil {
		return strings.ToValidUTF8(string(data), "")
	}
	return string(s)
}

// decodeCharset converts text in the named charset to UTF-8. It never fails:
// a missing or ASCII charset with 8-bit bytes is read as UTF-8 if valid and
// Windows-1252 otherwise, and an unknown charset is read as UTF-8 with invalid
// bytes dropped.
func decodeCharset(data []byte, cs string) string {
	cs = normalizeCharset(cs)
	var s string
	switch {
	case isUTF8Label(cs):
		data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
		s = string(data)
	case isASCIILabel(cs):
		data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
		if utf8.Valid(data) {
			s = string(data)
		} else {
			s = win1252(data)
		}
	case isLatin1Label(cs):
		s = win1252(data)
	default:
		r, err := charset.Reader(cs, bytes.NewReader(data))
		if err != nil {
			s = string(data)
		} else {
			// Truncated multibyte input may end with an error; keep what
			// was decoded.
			b, _ := io.ReadAll(r)
			s = string(b)
		}
	}
	return strings.ToValidUTF8(s, "")
}

// decodeHTMLCharset decodes an HTML body. Without a MIME charset, valid UTF-8
// is taken as is; otherwise the document's <meta> charset is used.
func decodeHTMLCharset(data []byte, cs string) string {
	if normalizeCharset(cs) != "" || utf8.Valid(data) {
		return decodeCharset(data, cs)
	}
	enc, _, _ := htmlcharset.DetermineEncoding(data, "text/html")
	if enc != nil {
		if b, err := enc.NewDecoder().Bytes(data); err == nil {
			return strings.ToValidUTF8(string(b), "")
		}
	}
	return decodeCharset(data, "")
}

// wordDecoder decodes RFC 2047 encoded-words. Unknown charsets never fail the
// whole header: their bytes are read as UTF-8 instead.
var wordDecoder = &mime.WordDecoder{
	CharsetReader: func(cs string, input io.Reader) (io.Reader, error) {
		b, _ := io.ReadAll(input)
		return strings.NewReader(decodeCharset(b, cs)), nil
	},
}

// decodeHeaderText decodes a header value to UTF-8: raw 8-bit bytes that are
// not UTF-8 are read in the hint charset (Windows-1252 if none), then RFC 2047
// encoded-words are decoded.
func decodeHeaderText(v, hint string) string {
	if !utf8.ValidString(v) {
		if normalizeCharset(hint) == "" || isUTF8Label(normalizeCharset(hint)) {
			hint = "windows-1252"
		}
		v = decodeCharset([]byte(v), hint)
	}
	if strings.Contains(v, "=?") {
		if dec, err := wordDecoder.DecodeHeader(v); err == nil {
			v = dec
		}
	}
	return fixC1(strings.ToValidUTF8(v, ""))
}

// fixC1 maps C1 control characters (U+0080-U+009F), which only appear when
// Windows-1252 text was labelled ISO-8859-1, to the characters meant.
func fixC1(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r >= 0x80 && r <= 0x9f }) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if r >= 0x80 && r <= 0x9f {
			if m := charmap.Windows1252.DecodeByte(byte(r)); m != utf8.RuneError && (m < 0x80 || m > 0x9f) {
				b.WriteRune(m)
				continue
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

// normalizeNewlines converts CRLF and lone CR line endings to LF.
func normalizeNewlines(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}
