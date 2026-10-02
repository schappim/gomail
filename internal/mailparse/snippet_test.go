package mailparse

import (
	"encoding/base64"
	"mime/quotedprintable"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/encoding/japanese"
)

func collapseWS(s string) string { return strings.Join(strings.Fields(s), " ") }

func qpEncode(t *testing.T, s string) string {
	t.Helper()
	var b strings.Builder
	w := quotedprintable.NewWriter(&b)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return b.String()
}

func checkPrefix(t *testing.T, label string, got, full string) {
	t.Helper()
	if !utf8.ValidString(got) {
		t.Fatalf("%s: invalid UTF-8 %q", label, got)
	}
	if !strings.HasPrefix(full, got) {
		t.Fatalf("%s: %q is not a prefix of %q", label, got, full)
	}
}

func TestSnippetBasic(t *testing.T) {
	tests := []struct {
		name              string
		data, enc, mt, cs string
		max               int
		want              string
	}{
		{"plain 7bit", "Hello   world,\r\n\r\nhow are\tyou?", "7bit", "text/plain", "us-ascii", 100, "Hello world, how are you?"},
		{"cut to runes", "Hello world", "", "text/plain", "", 5, "Hello"},
		{"cut does not leave trailing space", "Hello world", "", "text/plain", "", 6, "Hello"},
		{"no limit", "a b c", "", "text/plain", "", 0, "a b c"},
		{"multibyte cut", "日本語のテキスト", "8bit", "text/plain", "utf-8", 3, "日本語"},
		{"latin1", "Gr\xfc\xdfe", "8bit", "text/plain", "ISO-8859-1", 50, "Grüße"},
		{"smart quotes cp1252", "\x93Hi\x94", "8bit", "text/plain", "windows-1252", 50, "“Hi”"},
		{"unknown charset", "ok\xff\xfe text", "8bit", "text/plain", "x-unknown-thing", 50, "ok text"},
		{"no charset 8bit utf8", "café", "", "text/plain", "", 50, "café"},
		{"no charset latin1 bytes", "caf\xe9", "", "text/plain", "", 50, "café"},
		{"quoted lines dropped", "Thanks, sounds good.\n\nOn Mon, Bob wrote:\n> Lunch?\n> > Earlier\n", "7bit", "text/plain", "", 100, "Thanks, sounds good. On Mon, Bob wrote:"},
		{"only quoted lines kept", "> just a quote\n> second", "7bit", "text/plain", "", 100, "just a quote second"},
		{"html", `<html><head><style>p{}</style></head><body><div style="display:none">pre</div><p>Hi &amp; welcome</p><blockquote>old</blockquote></body></html>`, "", "text/html", "utf-8", 100, "Hi & welcome"},
		{"html media type case and params", "<p>Hi</p>", "", "TEXT/HTML; charset=utf-8", "", 100, "Hi"},
		{"base64 utf8", base64.StdEncoding.EncodeToString([]byte("Grüße aus München")), "BASE64", "text/plain", "utf-8", 100, "Grüße aus München"},
		{"qp", "Gr=C3=BC=C3=9Fe =\r\naus", "quoted-printable", "text/plain", "utf-8", 100, "Grüße aus"},
		{"qp trailing partial escape", "abc=C", "quoted-printable", "text/plain", "utf-8", 100, "abc"},
		{"qp trailing equals", "abc=", "quoted-printable", "text/plain", "utf-8", 100, "abc"},
		{"qp trailing equals CR", "abc=\r", "quoted-printable", "text/plain", "utf-8", 100, "abc"},
		{"empty", "", "base64", "text/html", "", 100, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecodeSnippet([]byte(tt.data), tt.enc, tt.mt, tt.cs, tt.max)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSnippetTruncatedBase64 cuts a base64 body at every possible offset:
// the result must always be valid UTF-8 and a prefix of the full text.
func TestSnippetTruncatedBase64(t *testing.T) {
	text := "Grüße aus München — 日本語のテキスト 🎉 and some more ASCII text to fill the line. Ende."
	enc := wrap76(base64.StdEncoding.EncodeToString([]byte(text)))
	enc = strings.ReplaceAll(enc, "\n", "\r\n")
	full := collapseWS(text)
	if got := DecodeSnippet([]byte(enc), "base64", "text/plain", "utf-8", 0); got != full {
		t.Fatalf("full decode = %q", got)
	}
	for i := 0; i <= len(enc); i++ {
		got := DecodeSnippet([]byte(enc[:i]), "base64", "text/plain", "UTF-8", 0)
		checkPrefix(t, "base64 cut "+strconv.Itoa(i), got, full)
	}
	// Decoding stops at the last complete quantum: 7 base64 chars give 3 bytes.
	if got := DecodeSnippet([]byte("SGVsbG8"), "base64", "text/plain", "", 0); got != "Hel" {
		t.Errorf("partial quantum: %q", got)
	}
}

func TestSnippetTruncatedQP(t *testing.T) {
	text := "Grüße aus München, schöne Straßen und Cafés. " + strings.Repeat("Mehr Text äöü. ", 8)
	enc := qpEncode(t, text)
	full := collapseWS(text)
	if got := DecodeSnippet([]byte(enc), "quoted-printable", "text/plain", "utf-8", 0); got != full {
		t.Fatalf("full decode = %q, want %q", got, full)
	}
	for i := 0; i <= len(enc); i++ {
		got := DecodeSnippet([]byte(enc[:i]), "quoted-printable", "text/plain", "utf-8", 0)
		checkPrefix(t, "qp cut "+strconv.Itoa(i), got, full)
		if strings.Contains(got, "=") {
			t.Fatalf("qp cut %d left an escape: %q", i, got)
		}
	}
}

func TestSnippetTruncatedHTML(t *testing.T) {
	html := `<!DOCTYPE html><html><head><title>Newsletter</title><style type="text/css">body { font-family: Arial; } .x { color: #333; }</style></head>
<body><div style="display:none;font-size:1px">Preheader you should not see</div>
<table width="100%"><tr><td><a href="https://example.com/track?id=1&amp;u=2"><img src="logo.png" alt="Acme"></a></td></tr>
<tr><td><h1>Spring Sale</h1><p>Everything is 50&nbsp;% off this week only.</p><p>Shop <a href="https://example.com/shop">now</a>!</p></td></tr></table></body></html>`
	full := DecodeSnippet([]byte(html), "", "text/html", "utf-8", 0)
	if !strings.HasPrefix(full, "[image: Acme] (https://example.com/track?id=1&u=2) Spring Sale Everything is 50 % off") {
		t.Fatalf("full = %q", full)
	}
	for i := 0; i <= len(html); i++ {
		got := DecodeSnippet([]byte(html[:i]), "", "text/html", "utf-8", 0)
		if !utf8.ValidString(got) {
			t.Fatalf("cut %d: invalid UTF-8", i)
		}
		for _, bad := range []string{"<", "font-family", "Preheader", "Newsletter"} {
			if strings.Contains(got, bad) {
				t.Fatalf("cut %d: snippet %q contains %q", i, got, bad)
			}
		}
	}
	// Base64-encoded HTML cut mid-stream.
	enc := base64.StdEncoding.EncodeToString([]byte(html))
	got := DecodeSnippet([]byte(enc[:len(enc)/2+1]), "base64", "text/html", "utf-8", 40)
	if utf8.RuneCountInString(got) > 40 || strings.Contains(got, "<") {
		t.Errorf("base64 html snippet = %q", got)
	}
}

func TestSnippetTruncatedShiftJIS(t *testing.T) {
	text := "こんにちは、世界。テストメールです。"
	sjis, err := japanese.ShiftJIS.NewEncoder().String(text)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= len(sjis); i++ {
		got := DecodeSnippet([]byte(sjis[:i]), "8bit", "text/plain", "Shift_JIS", 0)
		checkPrefix(t, "sjis cut "+strconv.Itoa(i), got, text)
	}
	enc := base64.StdEncoding.EncodeToString([]byte(sjis))
	if got := DecodeSnippet([]byte(enc), "base64", "text/plain", "shift_jis", 5); got != "こんにちは" {
		t.Errorf("got %q", got)
	}
}

func FuzzDecodeSnippet(f *testing.F) {
	f.Add([]byte("SGVsbG8gd29ybGQ="), "base64", "text/plain", "utf-8", 10)
	f.Add([]byte("Gr=C3=BC=C3=9Fe=\r\n"), "quoted-printable", "text/html", "", 50)
	f.Add([]byte("<p>Hi</p><blockquote>x"), "", "text/html", "shift_jis", 0)
	f.Fuzz(func(t *testing.T, data []byte, enc, mt, cs string, max int) {
		got := DecodeSnippet(data, enc, mt, cs, max)
		if !utf8.ValidString(got) {
			t.Fatalf("invalid UTF-8: %q", got)
		}
		if max > 0 && utf8.RuneCountInString(got) > max {
			t.Fatalf("snippet longer than %d runes: %q", max, got)
		}
		if got != strings.TrimSpace(got) || strings.Contains(got, "  ") {
			t.Fatalf("whitespace not collapsed: %q", got)
		}
	})
}
