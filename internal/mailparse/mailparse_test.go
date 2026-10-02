package mailparse

import (
	"bytes"
	"encoding/base64"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"

	"gomail/internal/model"
)

// crlf converts a message written with LF line endings to the CRLF line
// endings used on the wire.
func crlf(s string) []byte {
	return []byte(strings.ReplaceAll(s, "\n", "\r\n"))
}

func mustParse(t *testing.T, raw []byte) *model.Message {
	t.Helper()
	m, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return m
}

func encode(t *testing.T, enc encoding.Encoding, s string) string {
	t.Helper()
	b, err := enc.NewEncoder().String(s)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return b
}

func wrap76(s string) string {
	var b strings.Builder
	for len(s) > 76 {
		b.WriteString(s[:76] + "\n")
		s = s[76:]
	}
	b.WriteString(s)
	return b.String()
}

func addrs(list ...string) []model.Address {
	var out []model.Address
	for i := 0; i+1 < len(list); i += 2 {
		out = append(out, model.Address{Name: list[i], Email: list[i+1]})
	}
	return out
}

func checkAddrs(t *testing.T, field string, got, want []model.Address) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %#v, want %#v", field, got, want)
	}
}

func TestPlainASCII(t *testing.T) {
	raw := crlf(`Received: from mail.example.com by mx.google.com; Tue, 05 Mar 2024 09:15:02 -0800
Received: from localhost by mail.example.com; Tue, 05 Mar 2024 09:15:01 -0800
Return-Path: <alice@example.com>
From: Alice Example <alice@example.com>
To: bob@example.org, "Carol C." <carol@example.net>
Cc: Dave <dave@example.com>
Reply-To: replies@example.com
Subject: Lunch on Friday?
Date: Tue, 5 Mar 2024 09:15:00 -0800
Message-ID: <CAF1234.5678@mail.example.com>
In-Reply-To: <prev-1@example.org>
References: <root-0@example.org>
 <prev-1@example.org>
X-Mailer: Example Mailer 1.0
MIME-Version: 1.0
Content-Type: text/plain; charset=us-ascii

Hi Bob,

Are you free for lunch on Friday?

` + "-- " + `
Alice
`)
	m := mustParse(t, raw)
	if m.Subject != "Lunch on Friday?" {
		t.Errorf("Subject = %q", m.Subject)
	}
	checkAddrs(t, "From", m.From, addrs("Alice Example", "alice@example.com"))
	checkAddrs(t, "To", m.To, addrs("", "bob@example.org", "Carol C.", "carol@example.net"))
	checkAddrs(t, "Cc", m.Cc, addrs("Dave", "dave@example.com"))
	checkAddrs(t, "ReplyTo", m.ReplyTo, addrs("", "replies@example.com"))
	if len(m.Bcc) != 0 {
		t.Errorf("Bcc = %v", m.Bcc)
	}
	wantDate := time.Date(2024, 3, 5, 17, 15, 0, 0, time.UTC)
	if !m.Date.Equal(wantDate) {
		t.Errorf("Date = %v, want %v", m.Date, wantDate)
	}
	if m.MessageID != "CAF1234.5678@mail.example.com" {
		t.Errorf("MessageID = %q", m.MessageID)
	}
	if m.InReplyTo != "prev-1@example.org" {
		t.Errorf("InReplyTo = %q", m.InReplyTo)
	}
	if want := []string{"root-0@example.org", "prev-1@example.org"}; !reflect.DeepEqual(m.References, want) {
		t.Errorf("References = %q", m.References)
	}
	wantText := "Hi Bob,\n\nAre you free for lunch on Friday?\n\n-- \nAlice"
	if m.Text != wantText {
		t.Errorf("Text = %q, want %q", m.Text, wantText)
	}
	if m.HTML != "" || m.TextFromHTML || m.HasAttach || len(m.Attachments) != 0 {
		t.Errorf("unexpected HTML/attachments: %+v", m)
	}
	if m.Attachments == nil {
		t.Errorf("Attachments should be an empty slice, not nil")
	}
	if m.Size != uint32(len(raw)) || !bytes.Equal(m.Raw, raw) {
		t.Errorf("Size/Raw not set")
	}
	// Full header list, in order, original capitalisation, unfolded.
	if len(m.Headers) != 15 {
		t.Fatalf("got %d headers", len(m.Headers))
	}
	if m.Headers[0].Name != "Received" || m.Headers[1].Name != "Received" || !strings.HasPrefix(m.Headers[1].Value, "from localhost") {
		t.Errorf("Received headers wrong: %+v", m.Headers[:2])
	}
	if m.Headers[9].Name != "Message-ID" {
		t.Errorf("header name capitalisation not preserved: %q", m.Headers[9].Name)
	}
	if m.Header("references") != "<root-0@example.org> <prev-1@example.org>" {
		t.Errorf("References header not unfolded: %q", m.Header("references"))
	}
	if m.ID != "" || m.ThreadID != "" || m.Labels != nil || m.UID != 0 {
		t.Errorf("IMAP fields should be untouched")
	}
}

func TestLatin1QuotedPrintable(t *testing.T) {
	raw := crlf(`From: =?ISO-8859-1?Q?J=F6rg_M=FCller?= <joerg@example.de>
To: anna@example.de
Subject: =?ISO-8859-1?Q?Gr=FC=DFe_aus_M=FCnchen?=
Date: Wed, 6 Mar 2024 10:00:00 +0100
Content-Type: text/plain; charset="ISO-8859-1"
Content-Transfer-Encoding: quoted-printable

Liebe Anna,=20

viele Gr=FC=DFe aus M=FCnchen! Das Wetter ist sch=F6n und die Stra=DFen =
sind voller Leute.

Sch=F6ne Gr=FC=DFe,
J=F6rg
`)
	m := mustParse(t, raw)
	if m.Subject != "Grüße aus München" {
		t.Errorf("Subject = %q", m.Subject)
	}
	checkAddrs(t, "From", m.From, addrs("Jörg Müller", "joerg@example.de"))
	want := "Liebe Anna, \n\nviele Grüße aus München! Das Wetter ist schön und die Straßen sind voller Leute.\n\nSchöne Grüße,\nJörg"
	if m.Text != want {
		t.Errorf("Text =\n%q\nwant\n%q", m.Text, want)
	}
}

func TestWindows1252SmartQuotes(t *testing.T) {
	for _, cs := range []string{"windows-1252", "iso-8859-1", "us-ascii", ""} {
		t.Run(cs, func(t *testing.T) {
			ct := "text/plain"
			if cs != "" {
				ct += "; charset=" + cs
			}
			raw := crlf("From: a@example.com\nSubject: =?windows-1252?Q?It=92s_=93quoted=94?=\nContent-Type: " + ct +
				"\nContent-Transfer-Encoding: 8bit\n\n\x93Hello\x94 \x96 it\x92s \x85 \x80100\n")
			m := mustParse(t, raw)
			if want := "\u201cHello\u201d \u2013 it\u2019s \u2026 \u20ac100"; m.Text != want {
				t.Errorf("Text = %q, want %q", m.Text, want)
			}
			if want := "It\u2019s \u201cquoted\u201d"; m.Subject != want {
				t.Errorf("Subject = %q, want %q", m.Subject, want)
			}
		})
	}
}

func TestUTF8Base64Body(t *testing.T) {
	body := "Hello 世界! Ünïcödé text with emoji 🎉 and a long line that goes past seventy-six characters to force wrapping.\nSecond line."
	raw := crlf(`From: a@example.com
Subject: base64
Content-Type: text/plain; charset=UTF-8
Content-Transfer-Encoding: base64

` + wrap76(base64.StdEncoding.EncodeToString([]byte(body))) + "\n")
	m := mustParse(t, raw)
	if m.Text != body {
		t.Errorf("Text = %q, want %q", m.Text, body)
	}
}

func TestRFC2047Subjects(t *testing.T) {
	tests := []struct {
		name, header, want string
	}{
		{"B encoding", "=?UTF-8?B?w4RwZmVsIHVuZCBCaXJuZW4=?=", "Äpfel und Birnen"},
		{"Q encoding", "=?iso-8859-1?q?Caf=E9_cr=E8me?=", "Café crème"},
		{"mixed with plain text", "Re: =?UTF-8?Q?Gr=C3=BC=C3=9Fe?= aus =?UTF-8?Q?M=C3=BCnchen?= (fwd)", "Re: Grüße aus München (fwd)"},
		{"adjacent words joined", "=?UTF-8?B?5pel5pys6Kqe44Gu?= =?UTF-8?B?5Lu25ZCN44Gn44GZ?=", "日本語の件名です"},
		{"folded adjacent words", "=?utf-8?q?Hello?=\r\n =?utf-8?q?_World?=", "Hello World"},
		{"multibyte split across words", "=?UTF-8?Q?Gr=C3?= =?UTF-8?Q?=BC=C3=9Fe?=", "Grüße"},
		{"lowercase charset and encoding", "=?utf-8?b?w4RwZmVs?=", "Äpfel"},
		{"windows-1252 smart quote", "=?windows-1252?Q?It=92s_here?=", "It’s here"},
		{"latin1 label with cp1252 byte", "=?iso-8859-1?Q?It=92s?=", "It’s"},
		{"unknown charset", "=?x-klingon?Q?plain_ascii?=", "plain ascii"},
		{"language suffix", "=?utf-8*en?q?hello?=", "hello"},
		{"malformed word kept", "=?utf-8?B?not base64!?=", "=?utf-8?B?not base64!?="},
		{"raw utf-8", "Grüße ✓", "Grüße ✓"},
		{"raw latin-1 bytes", "Gr\xfc\xdfe", "Grüße"},
		{"iso-2022-jp", "=?ISO-2022-JP?B?" + base64.StdEncoding.EncodeToString([]byte(encode(t, japanese.ISO2022JP, "日本語"))) + "?=", "日本語"},
		{"gb2312", "=?GB2312?B?" + base64.StdEncoding.EncodeToString([]byte(encode(t, simplifiedchinese.GBK, "你好"))) + "?=", "你好"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte("From: a@example.com\r\nSubject: " + tt.header + "\r\n\r\nbody\r\n")
			m := mustParse(t, raw)
			if m.Subject != tt.want {
				t.Errorf("Subject = %q, want %q", m.Subject, tt.want)
			}
		})
	}
}

func TestEncodedDisplayNames(t *testing.T) {
	raw := crlf(`From: =?UTF-8?Q?J=C3=B6rg_M=C3=BCller?= <joerg@example.de>
To: =?ISO-8859-1?Q?Andr=E9?= <andre@example.fr>, "Smith, John" <john@example.com>
Cc: "=?UTF-8?B?55Sw5LitIOWkqumDjg==?=" <tanaka@example.jp>, =?UTF-8?B?Wm/DqyDDhW5nc3Ryw7Zt?= <zoe@example.se>
Reply-To: Zoë Ångström <zoe@example.se>
Bcc: Ren` + "\xe9" + ` Dupont <rene@example.fr>
Subject: names

body
`)
	m := mustParse(t, raw)
	checkAddrs(t, "From", m.From, addrs("Jörg Müller", "joerg@example.de"))
	checkAddrs(t, "To", m.To, addrs("André", "andre@example.fr", "Smith, John", "john@example.com"))
	checkAddrs(t, "Cc", m.Cc, addrs("田中 太郎", "tanaka@example.jp", "Zoë Ångström", "zoe@example.se"))
	checkAddrs(t, "ReplyTo", m.ReplyTo, addrs("Zoë Ångström", "zoe@example.se"))
	checkAddrs(t, "Bcc", m.Bcc, addrs("René Dupont", "rene@example.fr"))
	if got := m.Header("Bcc"); got != "René Dupont <rene@example.fr>" {
		t.Errorf("Bcc header value = %q", got)
	}
}

func TestMalformedAddressLists(t *testing.T) {
	tests := []struct {
		name, header string
		want         []model.Address
	}{
		{"undisclosed recipients", "undisclosed-recipients:;", nil},
		{"undisclosed with space", "undisclosed-recipients: ;", nil},
		{"group with members", "Team: a@example.com, Bob <b@example.com>;", addrs("", "a@example.com", "Bob", "b@example.com")},
		{"unquoted comma-free name with dot", "John Q. Public <jqp@example.com>", addrs("John Q. Public", "jqp@example.com")},
		{"missing closing quote", `"Unclosed <c@example.com>, e@example.com`, addrs("Unclosed", "c@example.com", "", "e@example.com")},
		{"comment form", "jdoe@example.com (John Doe)", addrs("John Doe", "jdoe@example.com")},
		{"semicolon separated", "a@example.com; b@example.com", addrs("", "a@example.com", "", "b@example.com")},
		{"trailing comma", "a@example.com, ", addrs("", "a@example.com")},
		{"brackets only", "<x@example.com>", addrs("", "x@example.com")},
		{"empty", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte("From: a@example.com\r\nTo: " + tt.header + "\r\nSubject: x\r\n\r\nbody\r\n")
			m := mustParse(t, raw)
			checkAddrs(t, "To", m.To, tt.want)
			if m.Text != "body" {
				t.Errorf("body lost: %q", m.Text)
			}
		})
	}
}

func TestMultipartAlternative(t *testing.T) {
	raw := crlf(`From: Bob <bob@example.com>
To: alice@example.com
Subject: Re: Lunch
Content-Type: multipart/alternative; boundary="000000000000a1b2c3"

--000000000000a1b2c3
Content-Type: text/plain; charset="UTF-8"

Sounds good!

On Tue, Alice wrote:
> Are you free?

--000000000000a1b2c3
Content-Type: text/html; charset="UTF-8"
Content-Transfer-Encoding: quoted-printable

<div dir=3D"ltr">Sounds good!</div><br><div class=3D"gmail_quote"><blockquote=
 class=3D"gmail_quote">Are you free?</blockquote></div>

--000000000000a1b2c3
Content-Type: text/x-amp-html; charset="UTF-8"

<!doctype html><html amp4email><body>AMP</body></html>

--000000000000a1b2c3--
`)
	m := mustParse(t, raw)
	if want := "Sounds good!\n\nOn Tue, Alice wrote:\n> Are you free?"; m.Text != want {
		t.Errorf("Text = %q, want %q", m.Text, want)
	}
	wantHTML := `<div dir="ltr">Sounds good!</div><br><div class="gmail_quote"><blockquote class="gmail_quote">Are you free?</blockquote></div>`
	if m.HTML != wantHTML {
		t.Errorf("HTML = %q", m.HTML)
	}
	if m.TextFromHTML {
		t.Errorf("TextFromHTML should be false")
	}
	if len(m.Attachments) != 0 || m.HasAttach {
		t.Errorf("alternative renderings must not be attachments: %+v", m.Attachments)
	}
}

func TestAlternativePrefersLastOfEachKind(t *testing.T) {
	raw := crlf(`From: a@example.com
Subject: x
Content-Type: multipart/alternative; boundary=b

--b
Content-Type: text/plain

first plain
--b
Content-Type: text/html

<p>first html</p>
--b
Content-Type: text/plain

second plain
--b
Content-Type: text/html

<p>second html</p>
--b--
`)
	m := mustParse(t, raw)
	if m.Text != "second plain" || m.HTML != "<p>second html</p>" {
		t.Errorf("Text=%q HTML=%q", m.Text, m.HTML)
	}
	if len(m.Attachments) != 0 {
		t.Errorf("attachments: %+v", m.Attachments)
	}
}

func TestMixedWithRFC2231PDF(t *testing.T) {
	pdf := "%PDF-1.4\n%\xe2\xe3\xcf\xd3\n1 0 obj << /Type /Catalog >> endobj\ntrailer << /Root 1 0 R >>\n%%EOF\n"
	raw := crlf(`From: billing@example.de
To: kunde@example.de
Subject: Ihre Rechnung
MIME-Version: 1.0
Content-Type: multipart/mixed;
 boundary="----=_Part_12345_67890.1709712000000"

This is a multi-part message in MIME format.

------=_Part_12345_67890.1709712000000
Content-Type: text/plain; charset=utf-8
Content-Transfer-Encoding: 8bit

Anbei Ihre Rechnung für März.
------=_Part_12345_67890.1709712000000
Content-Type: application/pdf; name="=?UTF-8?Q?Rechnung_f=C3=BCr_M=C3=A4rz.pdf?="
Content-Transfer-Encoding: base64
Content-Disposition: attachment;
 filename*0*=UTF-8''Rechnung%20f%C3%BCr;
 filename*1*=%20M%C3%A4rz.pdf

` + wrap76(base64.StdEncoding.EncodeToString([]byte(pdf))) + `
------=_Part_12345_67890.1709712000000--

epilogue text is ignored
`)
	m := mustParse(t, raw)
	if m.Text != "Anbei Ihre Rechnung für März." {
		t.Errorf("Text = %q", m.Text)
	}
	if len(m.Attachments) != 1 {
		t.Fatalf("attachments = %+v", m.Attachments)
	}
	a := m.Attachments[0]
	if a.Index != 1 || a.Filename != "Rechnung für März.pdf" || a.ContentType != "application/pdf" || a.Inline {
		t.Errorf("attachment = %+v", a)
	}
	if string(a.Data) != pdf || a.Size != len(pdf) {
		t.Errorf("attachment data mismatch: %q", a.Data)
	}
	if !m.HasAttach {
		t.Errorf("HasAttach should be true")
	}
}

func TestAttachmentFilenames(t *testing.T) {
	part := func(headers string) string {
		return "--b\n" + headers + "\nContent-Transfer-Encoding: base64\n\naGVsbG8=\n"
	}
	raw := crlf("From: a@example.com\nSubject: names\nContent-Type: multipart/mixed; boundary=b\n\n" +
		"--b\nContent-Type: text/plain\n\nbody\n" +
		part(`Content-Type: application/octet-stream; name="=?UTF-8?B?w4RwZmVs?=.txt"`) +
		part(`Content-Type: text/plain; charset=iso-8859-1
Content-Disposition: attachment; filename*=iso-8859-1''Gr%FC%DFe.txt`) +
		part(`Content-Type: application/x-msdownload
Content-Disposition: attachment; filename="C:\\Users\\bob\\evil.exe"`) +
		part(`Content-Type: application/octet-stream
Content-Disposition: attachment; filename="../../etc/passwd"`) +
		part(`Content-Type: image/png`) +
		part(`Content-Type: image/jpeg; name=photo.jpg`) +
		part(`Content-Type: application/pdf; name=unquoted name.pdf`) +
		part(`Content-Type: application/x-gomail-test-unknown`) +
		part(`Content-Type: application/octet-stream`) +
		part(`Content-Type: application/vnd.openxmlformats-officedocument.spreadsheetml.sheet`) +
		part(`Content-Type: application/octet-stream
Content-Disposition: attachment; filename="/"`) +
		"--b--\n")
	m := mustParse(t, raw)
	want := []string{
		"Äpfel.txt",
		"Grüße.txt",
		"evil.exe",
		"passwd",
		"attachment-5.png",
		"photo.jpg",
		"unquoted name.pdf",
		"attachment-8",
		"attachment-9.bin",
		"attachment-10.xlsx",
		"attachment-11.bin",
	}
	if len(m.Attachments) != len(want) {
		t.Fatalf("got %d attachments: %+v", len(m.Attachments), m.Attachments)
	}
	for i, a := range m.Attachments {
		if a.Index != i+1 {
			t.Errorf("attachment %d has Index %d", i, a.Index)
		}
		if a.Filename != want[i] {
			t.Errorf("attachment %d filename = %q, want %q", i+1, a.Filename, want[i])
		}
		if string(a.Data) != "hello" {
			t.Errorf("attachment %d data = %q", i+1, a.Data)
		}
	}
	if m.Text != "body" {
		t.Errorf("Text = %q", m.Text)
	}
}

func TestRelatedInlineImageInAlternative(t *testing.T) {
	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"
	raw := crlf(`From: news@example.com
Subject: Newsletter
Content-Type: multipart/alternative; boundary="alt"

--alt
Content-Type: text/plain; charset=utf-8

Our newsletter.
--alt
Content-Type: multipart/related; boundary="rel"; type="text/html"

--rel
Content-Type: text/html; charset=utf-8

<html><body><img src="cid:logo@example.com" alt="Logo"><p>Our newsletter.</p></body></html>
--rel
Content-Type: image/png
Content-Transfer-Encoding: base64
Content-ID: <logo@example.com>

` + base64.StdEncoding.EncodeToString([]byte(png)) + `
--rel--
--alt--
`)
	m := mustParse(t, raw)
	if m.Text != "Our newsletter." {
		t.Errorf("Text = %q", m.Text)
	}
	if !strings.Contains(m.HTML, `cid:logo@example.com`) {
		t.Errorf("HTML = %q", m.HTML)
	}
	if len(m.Attachments) != 1 {
		t.Fatalf("attachments = %+v", m.Attachments)
	}
	a := m.Attachments[0]
	if a.ContentID != "logo@example.com" || !a.Inline || a.ContentType != "image/png" || a.Filename != "attachment-1.png" || string(a.Data) != png {
		t.Errorf("attachment = %+v", a)
	}
	if m.HasAttach {
		t.Errorf("inline cid images should not set HasAttach")
	}
}

func TestRelatedStartParameter(t *testing.T) {
	raw := crlf(`From: a@example.com
Subject: x
Content-Type: multipart/related; boundary=r; start="<root@x>"

--r
Content-Type: image/gif
Content-ID: <img@x>
Content-Transfer-Encoding: base64

R0lGODlhAQABAAAAACw=
--r
Content-Type: text/html
Content-ID: <root@x>

<p>Root <img src="cid:img@x"></p>
--r--
`)
	m := mustParse(t, raw)
	if m.HTML != `<p>Root <img src="cid:img@x"></p>` || m.Text != "Root" || !m.TextFromHTML {
		t.Errorf("HTML=%q Text=%q", m.HTML, m.Text)
	}
	if len(m.Attachments) != 1 || m.Attachments[0].ContentID != "img@x" || !m.Attachments[0].Inline {
		t.Errorf("attachments = %+v", m.Attachments)
	}
}

func TestForwardedMessage(t *testing.T) {
	inner := "From: Carol <carol@example.com>\r\nTo: bob@example.com\r\nSubject: =?UTF-8?B?UXVhcnRlcmx5IHLDqXN1bcOp?=\r\nContent-Type: text/plain\r\n\r\nInner body text.\r\n"
	raw := crlf(`From: bob@example.com
To: alice@example.com
Subject: Fwd: Quarterly
Content-Type: multipart/mixed; boundary="fwd"

--fwd
Content-Type: text/plain

See the forwarded message.
--fwd
Content-Type: message/rfc822
Content-Disposition: inline

`) // the inner message keeps its own CRLFs
	raw = append(raw, []byte(inner+"\r\n--fwd\r\nContent-Type: message/rfc822\r\n\r\nFrom: x@example.com\r\nSubject: a/b: c\\d\r\n\r\nno subject chars\r\n--fwd\r\nContent-Type: message/rfc822\r\n\r\nFrom: x@example.com\r\n\r\nno subject\r\n--fwd--\r\n")...)
	m := mustParse(t, raw)
	if m.Text != "See the forwarded message." {
		t.Errorf("Text = %q (embedded body must not leak in)", m.Text)
	}
	if len(m.Attachments) != 3 {
		t.Fatalf("attachments = %+v", m.Attachments)
	}
	a := m.Attachments[0]
	if a.Filename != "Quarterly résumé.eml" || a.ContentType != "message/rfc822" || !a.Inline {
		t.Errorf("attachment = %+v", a)
	}
	if string(a.Data) != inner {
		t.Errorf("embedded data = %q", a.Data)
	}
	if got := m.Attachments[1].Filename; got != "a_b_ c_d.eml" {
		t.Errorf("sanitized subject filename = %q", got)
	}
	if got := m.Attachments[2].Filename; got != "forwarded-message.eml" {
		t.Errorf("fallback filename = %q", got)
	}
	if !m.HasAttach {
		t.Errorf("HasAttach should be true (non-inline forwards present)")
	}
}

func TestHTMLOnly(t *testing.T) {
	raw := crlf(`From: shop@example.com
Subject: Your order
Content-Type: text/html; charset=iso-8859-1
Content-Transfer-Encoding: quoted-printable

<html><head><style>p{margin:0}</style></head><body><div style=3D"display:non=
e">Preheader</div><p>Danke f=FCr Ihre Bestellung!</p><p><a href=3D"https://sho=
p.example.com/o/1">Bestellung ansehen</a></p></body></html>
`)
	m := mustParse(t, raw)
	if !m.TextFromHTML {
		t.Errorf("TextFromHTML should be true")
	}
	if want := "Danke für Ihre Bestellung!\n\nBestellung ansehen (https://shop.example.com/o/1)"; m.Text != want {
		t.Errorf("Text = %q, want %q", m.Text, want)
	}
	if !strings.Contains(m.HTML, "Danke für Ihre Bestellung!") {
		t.Errorf("HTML not decoded to UTF-8: %q", m.HTML)
	}
}

func TestEmptyPlainAlternativeUsesHTML(t *testing.T) {
	raw := crlf(`From: a@example.com
Subject: x
Content-Type: multipart/alternative; boundary=b

--b
Content-Type: text/plain



--b
Content-Type: text/html

<p>Real content</p>
--b--
`)
	m := mustParse(t, raw)
	if m.Text != "Real content" || !m.TextFromHTML {
		t.Errorf("Text = %q, TextFromHTML = %v", m.Text, m.TextFromHTML)
	}
}

func TestHTMLMetaCharset(t *testing.T) {
	// windows-1251 Cyrillic with no MIME charset, only a <meta> declaration.
	body := "<html><head><meta http-equiv=\"Content-Type\" content=\"text/html; charset=windows-1251\"></head><body>\xcf\xf0\xe8\xe2\xe5\xf2</body></html>"
	raw := crlf("From: a@example.com\nSubject: x\nContent-Type: text/html\n\n" + body + "\n")
	m := mustParse(t, raw)
	if m.Text != "Привет" {
		t.Errorf("Text = %q", m.Text)
	}
}

func TestMailingListFooterAfterHTML(t *testing.T) {
	raw := crlf(`From: a@example.com
Subject: x
Content-Type: multipart/mixed; boundary=b

--b
Content-Type: text/html

<p>Main message</p>
--b
Content-Type: text/plain

_______________________________________________
List footer
--b--
`)
	m := mustParse(t, raw)
	if want := "Main message\n\n_______________________________________________\nList footer"; m.Text != want {
		t.Errorf("Text = %q, want %q", m.Text, want)
	}
	if m.HTML != "<p>Main message</p>" || !m.TextFromHTML {
		t.Errorf("HTML = %q, TextFromHTML = %v", m.HTML, m.TextFromHTML)
	}
	if len(m.Attachments) != 0 {
		t.Errorf("attachments = %+v", m.Attachments)
	}
}

func TestBrokenMultipart(t *testing.T) {
	t.Run("missing boundary parameter is sniffed", func(t *testing.T) {
		raw := crlf(`From: a@example.com
Subject: x
Content-Type: multipart/mixed

--XyZ123
Content-Type: text/plain

Hello there
--XyZ123
Content-Type: application/pdf; name=a.pdf
Content-Transfer-Encoding: base64

aGVsbG8=
--XyZ123--
`)
		m := mustParse(t, raw)
		if m.Text != "Hello there" || len(m.Attachments) != 1 || m.Attachments[0].Filename != "a.pdf" {
			t.Errorf("Text=%q attachments=%+v", m.Text, m.Attachments)
		}
	})
	t.Run("wrong boundary parameter is sniffed", func(t *testing.T) {
		raw := crlf("From: a@example.com\nSubject: x\nContent-Type: multipart/alternative; boundary=\"nope\"\n\n--real\nContent-Type: text/plain\n\nplain\n--real\nContent-Type: text/html\n\n<b>html</b>\n--real--\n")
		m := mustParse(t, raw)
		if m.Text != "plain" || m.HTML != "<b>html</b>" {
			t.Errorf("Text=%q HTML=%q", m.Text, m.HTML)
		}
	})
	t.Run("no parts at all falls back to text", func(t *testing.T) {
		raw := crlf("From: a@example.com\nSubject: x\nContent-Type: multipart/mixed; boundary=\"gone\"\n\nJust some text, no MIME parts.\n-- \nsig\n")
		m := mustParse(t, raw)
		if m.Text != "Just some text, no MIME parts.\n-- \nsig" {
			t.Errorf("Text = %q", m.Text)
		}
	})
	t.Run("unquoted boundary with specials", func(t *testing.T) {
		raw := crlf("From: a@example.com\nSubject: x\nContent-Type: multipart/mixed; boundary=----=_NextPart_000_0001_01D9.ABCDEF00\n\n------=_NextPart_000_0001_01D9.ABCDEF00\nContent-Type: text/plain\n\nOutlook body\n------=_NextPart_000_0001_01D9.ABCDEF00--\n")
		m := mustParse(t, raw)
		if m.Text != "Outlook body" {
			t.Errorf("Text = %q", m.Text)
		}
	})
	t.Run("truncated message", func(t *testing.T) {
		full := crlf(`From: a@example.com
Subject: x
Content-Type: multipart/mixed; boundary=b

--b
Content-Type: multipart/alternative; boundary=inner

--inner
Content-Type: text/plain

Plain body
--inner
Content-Type: text/html

<p>HTML body</p>
--inner--
--b
Content-Type: application/pdf; name=doc.pdf
Content-Transfer-Encoding: base64

` + wrap76(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("0123456789"), 30))) + `
--b--
`)
		cut := full[:len(full)-200]
		m := mustParse(t, cut)
		if m.Text != "Plain body" || m.HTML != "<p>HTML body</p>" {
			t.Errorf("Text=%q HTML=%q", m.Text, m.HTML)
		}
		if len(m.Attachments) != 1 || m.Attachments[0].Filename != "doc.pdf" ||
			!bytes.HasPrefix(bytes.Repeat([]byte("0123456789"), 30), m.Attachments[0].Data) || len(m.Attachments[0].Data) == 0 {
			t.Errorf("truncated attachment = %+v", m.Attachments)
		}
		// Every truncation point must parse without error or panic.
		for i := 20; i < len(full); i += 7 {
			if _, err := Parse(full[:i]); err != nil {
				t.Fatalf("Parse(full[:%d]) error: %v", i, err)
			}
		}
	})
	t.Run("LF line endings", func(t *testing.T) {
		raw := []byte("From: a@example.com\nSubject: lf\nContent-Type: multipart/alternative; boundary=b\n\n--b\nContent-Type: text/plain\n\nunix\nlines\n--b\nContent-Type: text/html\n\n<p>unix</p>\n--b--\n")
		m := mustParse(t, raw)
		if m.Text != "unix\nlines" || m.HTML != "<p>unix</p>" || m.Subject != "lf" {
			t.Errorf("Text=%q HTML=%q", m.Text, m.HTML)
		}
	})
	t.Run("transfer-encoded multipart", func(t *testing.T) {
		inner := "--q\r\nContent-Type: text/plain\r\n\r\nencoded multipart\r\n--q--\r\n"
		raw := crlf("From: a@example.com\nSubject: x\nContent-Type: multipart/mixed; boundary=q\nContent-Transfer-Encoding: base64\n\n" + base64.StdEncoding.EncodeToString([]byte(inner)) + "\n")
		m := mustParse(t, raw)
		if m.Text != "encoded multipart" {
			t.Errorf("Text = %q", m.Text)
		}
	})
	t.Run("part without headers", func(t *testing.T) {
		raw := crlf("From: a@example.com\nSubject: x\nContent-Type: multipart/mixed; boundary=b\n\n--b\n\nno part header\n--b--\n")
		m := mustParse(t, raw)
		if m.Text != "no part header" {
			t.Errorf("Text = %q", m.Text)
		}
	})
}

func TestShiftJISBody(t *testing.T) {
	text := "こんにちは、世界。\nテストメールです。"
	raw := crlf("From: =?ISO-2022-JP?B?" + base64.StdEncoding.EncodeToString([]byte(encode(t, japanese.ISO2022JP, "山田"))) + "?= <yamada@example.jp>\nSubject: test\nContent-Type: text/plain; charset=Shift_JIS\nContent-Transfer-Encoding: 8bit\n\n" + encode(t, japanese.ShiftJIS, text) + "\n")
	m := mustParse(t, raw)
	if m.Text != text {
		t.Errorf("Text = %q, want %q", m.Text, text)
	}
	checkAddrs(t, "From", m.From, addrs("山田", "yamada@example.jp"))
}

func TestGB2312Body(t *testing.T) {
	text := "你好，世界！这是一封测试邮件。"
	raw := crlf("From: a@example.cn\nSubject: =?GB2312?B?" + base64.StdEncoding.EncodeToString([]byte(encode(t, simplifiedchinese.GBK, "测试"))) +
		"?=\nContent-Type: text/plain; charset=\"gb2312\"\nContent-Transfer-Encoding: base64\n\n" +
		wrap76(base64.StdEncoding.EncodeToString([]byte(encode(t, simplifiedchinese.GBK, text)))) + "\n")
	m := mustParse(t, raw)
	if m.Text != text {
		t.Errorf("Text = %q, want %q", m.Text, text)
	}
	if m.Subject != "测试" {
		t.Errorf("Subject = %q", m.Subject)
	}
}

func TestFormatFlowed(t *testing.T) {
	t.Run("delsp no", func(t *testing.T) {
		raw := crlf("From: a@example.com\nSubject: x\nContent-Type: text/plain; charset=utf-8; format=flowed\n\n" +
			"This is a long \nparagraph that \nflows.\n\nSecond paragraph.\n>Quoted \n>text here.\n>>Deeper \n>>quote.\n From stuffed line.\n-- \nSig line\n")
		m := mustParse(t, raw)
		want := "This is a long paragraph that flows.\n\nSecond paragraph.\n> Quoted text here.\n>> Deeper quote.\nFrom stuffed line.\n-- \nSig line"
		if m.Text != want {
			t.Errorf("Text =\n%q\nwant\n%q", m.Text, want)
		}
	})
	t.Run("delsp yes", func(t *testing.T) {
		raw := crlf("From: a@example.com\nSubject: x\nContent-Type: text/plain; charset=utf-8; format=flowed; delsp=yes\n\n" +
			"日本語の \nテキストです。\nlong \nword\n")
		m := mustParse(t, raw)
		if want := "日本語のテキストです。\nlongword"; m.Text != want {
			t.Errorf("Text = %q, want %q", m.Text, want)
		}
	})
	t.Run("quoted-printable flowed", func(t *testing.T) {
		raw := crlf("From: a@example.com\nSubject: x\nContent-Type: text/plain; charset=utf-8; format=flowed; DelSp=No\nContent-Transfer-Encoding: quoted-printable\n\n" +
			"Caf=C3=A9 au lait=20\nis nice.\n")
		m := mustParse(t, raw)
		if want := "Café au lait is nice."; m.Text != want {
			t.Errorf("Text = %q, want %q", m.Text, want)
		}
	})
}

func TestSeveralTextPartsInMixed(t *testing.T) {
	// Apple Mail splits plain text around inline images.
	raw := crlf(`From: a@example.com
Subject: Photos
Content-Type: multipart/mixed; boundary="Apple-Mail=_1"

--Apple-Mail=_1
Content-Transfer-Encoding: 7bit
Content-Type: text/plain; charset=us-ascii

Here is the first photo:

--Apple-Mail=_1
Content-Disposition: inline; filename=IMG_0001.jpeg
Content-Type: image/jpeg; name="IMG_0001.jpeg"
Content-Transfer-Encoding: base64

/9j/4AAQSkZJRg==
--Apple-Mail=_1
Content-Transfer-Encoding: 7bit
Content-Type: text/plain; charset=us-ascii

And the second one.

--Apple-Mail=_1--
`)
	m := mustParse(t, raw)
	if want := "Here is the first photo:\n\nAnd the second one."; m.Text != want {
		t.Errorf("Text = %q, want %q", m.Text, want)
	}
	if len(m.Attachments) != 1 || m.Attachments[0].Filename != "IMG_0001.jpeg" || !m.Attachments[0].Inline || m.HasAttach {
		t.Errorf("attachments = %+v HasAttach=%v", m.Attachments, m.HasAttach)
	}

	// The HTML side of an Apple Mail alternative: several HTML parts.
	raw2 := crlf(`From: a@example.com
Subject: Photos
Content-Type: multipart/alternative; boundary=alt

--alt
Content-Type: text/plain

one
two
--alt
Content-Type: multipart/mixed; boundary=mix

--mix
Content-Type: text/html

<p>one</p>
--mix
Content-Type: image/png; name=a.png
Content-Disposition: inline; filename=a.png

png
--mix
Content-Type: text/html

<p>two</p>
--mix--
--alt--
`)
	m2 := mustParse(t, raw2)
	if m2.Text != "one\ntwo" || m2.HTML != "<p>one</p>\n<p>two</p>" {
		t.Errorf("Text=%q HTML=%q", m2.Text, m2.HTML)
	}
	if len(m2.Attachments) != 1 || m2.Attachments[0].Filename != "a.png" {
		t.Errorf("attachments = %+v", m2.Attachments)
	}
}

func TestTextPartsWithAttachmentDisposition(t *testing.T) {
	raw := crlf(`From: a@example.com
Subject: notes
Content-Type: multipart/mixed; boundary=b

--b
Content-Type: text/plain; charset=utf-8

See attached notes.
--b
Content-Type: text/plain; charset=utf-8; name="notes.txt"
Content-Disposition: attachment; filename="notes.txt"

These are notes, not the body.
--b
Content-Type: text/html; charset=utf-8
Content-Disposition: attachment; filename="page.html"

<p>Attached page</p>
--b--
`)
	m := mustParse(t, raw)
	if m.Text != "See attached notes." || m.HTML != "" {
		t.Errorf("Text=%q HTML=%q", m.Text, m.HTML)
	}
	if len(m.Attachments) != 2 {
		t.Fatalf("attachments = %+v", m.Attachments)
	}
	if a := m.Attachments[0]; a.Filename != "notes.txt" || a.ContentType != "text/plain" || string(a.Data) != "These are notes, not the body." || a.Inline {
		t.Errorf("attachment 1 = %+v", a)
	}
	if a := m.Attachments[1]; a.Filename != "page.html" || a.ContentType != "text/html" {
		t.Errorf("attachment 2 = %+v", a)
	}
	if !m.HasAttach {
		t.Errorf("HasAttach should be true")
	}
}

func TestCalendarInvite(t *testing.T) {
	ics := "BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nSUMMARY:Planning\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	raw := crlf(`From: Google Calendar <calendar-notification@google.com>
Subject: Invitation: Planning @ Wed 6 Mar 2024
Content-Type: multipart/mixed; boundary="000000000000mixed"

--000000000000mixed
Content-Type: multipart/alternative; boundary="000000000000alt"

--000000000000alt
Content-Type: text/plain; charset="UTF-8"

You have been invited to Planning.
--000000000000alt
Content-Type: text/html; charset="UTF-8"

<p>You have been invited to <b>Planning</b>.</p>
--000000000000alt
Content-Type: text/calendar; charset="UTF-8"; method=REQUEST
Content-Transfer-Encoding: 7bit

`) // ics keeps CRLF
	raw = append(raw, []byte(ics+"--000000000000alt--\r\n\r\n--000000000000mixed\r\nContent-Type: application/ics; name=\"invite.ics\"\r\nContent-Disposition: attachment; filename=\"invite.ics\"\r\nContent-Transfer-Encoding: base64\r\n\r\n"+
		base64.StdEncoding.EncodeToString([]byte(ics))+"\r\n--000000000000mixed--\r\n")...)
	m := mustParse(t, raw)
	if m.Text != "You have been invited to Planning." || !strings.Contains(m.HTML, "<b>Planning</b>") {
		t.Errorf("Text=%q HTML=%q", m.Text, m.HTML)
	}
	if len(m.Attachments) != 2 {
		t.Fatalf("attachments = %+v", m.Attachments)
	}
	cal := m.Attachments[0]
	if cal.ContentType != "text/calendar" || cal.Filename != "invite.ics" || cal.Inline || strings.TrimRight(string(cal.Data), "\r\n") != strings.TrimRight(ics, "\r\n") {
		t.Errorf("calendar attachment = %+v data=%q", cal, cal.Data)
	}
	if a := m.Attachments[1]; a.ContentType != "application/ics" || a.Filename != "invite.ics" || string(a.Data) != ics {
		t.Errorf("ics attachment = %+v", a)
	}
	if !m.HasAttach {
		t.Errorf("HasAttach should be true")
	}
}

func TestMultipartSigned(t *testing.T) {
	t.Run("pgp", func(t *testing.T) {
		raw := crlf(`From: a@example.com
Subject: signed
Content-Type: multipart/signed; micalg=pgp-sha256; protocol="application/pgp-signature"; boundary="sig"

--sig
Content-Type: multipart/alternative; boundary="alt"

--alt
Content-Type: text/plain; charset=utf-8

Signed text.
--alt
Content-Type: text/html; charset=utf-8

<p>Signed text.</p>
--alt--

--sig
Content-Type: application/pgp-signature; name="signature.asc"
Content-Description: OpenPGP digital signature
Content-Disposition: attachment; filename="signature.asc"

-----BEGIN PGP SIGNATURE-----
iQEzBAEBCAAdFiEE
-----END PGP SIGNATURE-----

--sig--
`)
		m := mustParse(t, raw)
		if m.Text != "Signed text." || m.HTML != "<p>Signed text.</p>" {
			t.Errorf("Text=%q HTML=%q", m.Text, m.HTML)
		}
		if len(m.Attachments) != 1 || m.Attachments[0].Filename != "signature.asc" || m.Attachments[0].ContentType != "application/pgp-signature" ||
			!strings.Contains(string(m.Attachments[0].Data), "BEGIN PGP SIGNATURE") {
			t.Errorf("attachments = %+v", m.Attachments)
		}
	})
	t.Run("smime", func(t *testing.T) {
		raw := crlf(`From: a@example.com
Subject: signed
Content-Type: multipart/signed; protocol="application/pkcs7-signature"; micalg=sha-256; boundary=s

--s
Content-Type: text/plain

S/MIME signed.
--s
Content-Type: application/pkcs7-signature
Content-Transfer-Encoding: base64

MIAGCSqGSIb3DQEHAqCAMIACAQEx
--s--
`)
		m := mustParse(t, raw)
		if m.Text != "S/MIME signed." {
			t.Errorf("Text = %q", m.Text)
		}
		if len(m.Attachments) != 1 || m.Attachments[0].Filename != "attachment-1.p7s" || m.Attachments[0].ContentType != "application/pkcs7-signature" {
			t.Errorf("attachments = %+v", m.Attachments)
		}
	})
}

func TestDeliveryReport(t *testing.T) {
	raw := crlf(`From: Mail Delivery Subsystem <mailer-daemon@googlemail.com>
Subject: Delivery Status Notification (Failure)
Content-Type: multipart/report; report-type=delivery-status; boundary=r

--r
Content-Type: text/plain

Your message wasn't delivered.
--r
Content-Type: message/delivery-status

Reporting-MTA: dns; googlemail.com
--r
Content-Type: text/rfc822-headers

From: a@example.com
Subject: original
--r--
`)
	m := mustParse(t, raw)
	if m.Text != "Your message wasn't delivered." {
		t.Errorf("Text = %q", m.Text)
	}
	if len(m.Attachments) != 2 || m.Attachments[0].Filename != "attachment-1.txt" || m.Attachments[1].ContentType != "text/rfc822-headers" {
		t.Errorf("attachments = %+v", m.Attachments)
	}
}

func TestMultipartDigest(t *testing.T) {
	raw := crlf(`From: list@example.com
Subject: Digest
Content-Type: multipart/mixed; boundary=outer

--outer
Content-Type: text/plain

Today's topics
--outer
Content-Type: multipart/digest; boundary=d

--d

From: one@example.com
Subject: First post

one
--d

From: two@example.com
Subject: Second post

two
--d--
--outer--
`)
	m := mustParse(t, raw)
	if m.Text != "Today's topics" {
		t.Errorf("Text = %q", m.Text)
	}
	if len(m.Attachments) != 2 || m.Attachments[0].Filename != "First post.eml" || m.Attachments[1].ContentType != "message/rfc822" {
		t.Errorf("attachments = %+v", m.Attachments)
	}
}

func TestSinglePartAttachment(t *testing.T) {
	raw := crlf("From: scanner@example.com\nSubject: Scan\nContent-Type: application/pdf\nContent-Disposition: attachment; filename=scan.pdf\nContent-Transfer-Encoding: base64\n\nJVBERi0xLjQ=\n")
	m := mustParse(t, raw)
	if m.Text != "" || len(m.Attachments) != 1 || m.Attachments[0].Filename != "scan.pdf" || string(m.Attachments[0].Data) != "%PDF-1.4" || !m.HasAttach {
		t.Errorf("Text=%q attachments=%+v", m.Text, m.Attachments)
	}
}

func TestUnknownCharsetAndEncoding(t *testing.T) {
	t.Run("unknown charset", func(t *testing.T) {
		raw := crlf("From: a@example.com\nSubject: x\nContent-Type: text/plain; charset=x-klingon\n\nqapla\xff' ok\n")
		m := mustParse(t, raw)
		if m.Text != "qapla' ok" {
			t.Errorf("Text = %q", m.Text)
		}
	})
	t.Run("unknown transfer encoding", func(t *testing.T) {
		raw := crlf("From: a@example.com\nSubject: x\nContent-Type: text/plain\nContent-Transfer-Encoding: x-gomail-weird\n\nraw text\n")
		m := mustParse(t, raw)
		if m.Text != "raw text" {
			t.Errorf("Text = %q", m.Text)
		}
	})
	t.Run("base64 with junk", func(t *testing.T) {
		raw := crlf("From: a@example.com\nSubject: x\nContent-Type: text/plain\nContent-Transfer-Encoding: Base64\n\naGVs bG8g\n  d29y\tbGQ=\n!!\n")
		m := mustParse(t, raw)
		if m.Text != "hello world" {
			t.Errorf("Text = %q", m.Text)
		}
	})
	t.Run("base64 padded per line", func(t *testing.T) {
		raw := crlf("From: a@example.com\nSubject: x\nContent-Transfer-Encoding: base64\n\naGk=\nIHRoZXJl\n")
		m := mustParse(t, raw)
		if m.Text != "hi there" {
			t.Errorf("Text = %q", m.Text)
		}
	})
	t.Run("broken quoted-printable", func(t *testing.T) {
		raw := crlf("From: a@example.com\nSubject: x\nContent-Type: text/plain; charset=utf-8\nContent-Transfer-Encoding: quoted-printable\n\n1+1=2 and =ZZ and =c3=a9 and soft= \nbreak=\n")
		m := mustParse(t, raw)
		if m.Text != "1+1=2 and =ZZ and é and softbreak" {
			t.Errorf("Text = %q", m.Text)
		}
	})
	t.Run("malformed content-type", func(t *testing.T) {
		raw := crlf("From: a@example.com\nSubject: x\nContent-Type: text; charset=\nContent-Disposition: inline;;\n\nstill text\n")
		m := mustParse(t, raw)
		if m.Text != "still text" {
			t.Errorf("Text = %q", m.Text)
		}
	})
}

func TestDates(t *testing.T) {
	utc := func(y int, mo time.Month, d, h, mi, s int) time.Time {
		return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
	}
	tests := []struct {
		in   string
		want time.Time
	}{
		{"Tue, 5 Mar 2024 09:15:00 -0800", utc(2024, 3, 5, 17, 15, 0)},
		{"Tue, 05 Mar 2024 09:15:00 -0800 (PST)", utc(2024, 3, 5, 17, 15, 0)},
		{"5 Mar 2024 17:15:00 GMT", utc(2024, 3, 5, 17, 15, 0)},
		{"Tue, 5 Mar 2024 17:15:00 +0000 GMT", utc(2024, 3, 5, 17, 15, 0)},
		{"Tuesday, 5 Mar 2024 17:15:00 +0000", utc(2024, 3, 5, 17, 15, 0)},
		{"Tue,  5 Mar 2024 17:15 +0000", utc(2024, 3, 5, 17, 15, 0)},
		{"Tue Mar  5 17:15:00 2024", utc(2024, 3, 5, 17, 15, 0)},
		{"2024-03-05T17:15:00Z", utc(2024, 3, 5, 17, 15, 0)},
		{"2024-03-05 18:15:00 +0100", utc(2024, 3, 5, 17, 15, 0)},
		{"Tue, 5 Mar 2024 18:15:00 +01:00", utc(2024, 3, 5, 17, 15, 0)},
		{"5 March 2024 17:15:00 +0000", utc(2024, 3, 5, 17, 15, 0)},
		{"not a date at all", time.Time{}},
		{"", time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			raw := []byte("From: a@example.com\r\nDate: " + tt.in + "\r\nSubject: x\r\n\r\nbody\r\n")
			m := mustParse(t, raw)
			if !m.Date.Equal(tt.want) {
				t.Errorf("Date(%q) = %v, want %v", tt.in, m.Date, tt.want)
			}
		})
	}
}

func TestMessageIDs(t *testing.T) {
	raw := crlf(`From: a@example.com
Subject: ids
Message-ID: no-brackets-123@example.com
In-Reply-To: Your message of "Mon, 4 Mar 2024" <orig@example.com>
References: <a@example.com>,<b@example.com>
	<c@[192.168.0.1]> junk

body
`)
	m := mustParse(t, raw)
	if m.MessageID != "no-brackets-123@example.com" {
		t.Errorf("MessageID = %q", m.MessageID)
	}
	if m.InReplyTo != "orig@example.com" {
		t.Errorf("InReplyTo = %q", m.InReplyTo)
	}
	if want := []string{"a@example.com", "b@example.com", "c@[192.168.0.1]"}; !reflect.DeepEqual(m.References, want) {
		t.Errorf("References = %q, want %q", m.References, want)
	}

	raw2 := crlf("From: a@example.com\nMessage-Id: <  spaced@example.com >\nIn-Reply-To: <x@example.com> (Bob's message)\n\nbody\n")
	m2 := mustParse(t, raw2)
	if m2.MessageID != "spaced@example.com" || m2.InReplyTo != "x@example.com" {
		t.Errorf("MessageID=%q InReplyTo=%q", m2.MessageID, m2.InReplyTo)
	}
}

func TestHeaderTolerance(t *testing.T) {
	t.Run("mbox From line and BOM", func(t *testing.T) {
		raw := []byte("\xef\xbb\xbfFrom alice@example.com Tue Mar  5 09:15:00 2024\nFrom: alice@example.com\nSubject: mbox\n\nbody\n")
		m := mustParse(t, raw)
		if m.Subject != "mbox" || m.Text != "body" || len(m.From) != 1 {
			t.Errorf("Subject=%q Text=%q From=%v", m.Subject, m.Text, m.From)
		}
	})
	t.Run("missing blank line before body", func(t *testing.T) {
		raw := crlf("From: a@example.com\nSubject: no blank\nHello, this line starts the body.\nSecond line.\n")
		m := mustParse(t, raw)
		if m.Subject != "no blank" || m.Text != "Hello, this line starts the body.\nSecond line." {
			t.Errorf("Subject=%q Text=%q", m.Subject, m.Text)
		}
	})
	t.Run("space before colon and stray continuation", func(t *testing.T) {
		raw := crlf("  stray continuation\nSubject : spaced key\nFrom: a@example.com\n\nbody\n")
		m := mustParse(t, raw)
		if m.Subject != "spaced key" || m.Headers[0].Name != "Subject" {
			t.Errorf("Subject=%q headers=%+v", m.Subject, m.Headers)
		}
	})
	t.Run("header only", func(t *testing.T) {
		m := mustParse(t, []byte("From: a@example.com\r\nSubject: header only"))
		if m.Subject != "header only" || m.Text != "" {
			t.Errorf("Subject=%q Text=%q", m.Subject, m.Text)
		}
	})
	t.Run("encoded header values in list", func(t *testing.T) {
		m := mustParse(t, crlf("From: a@example.com\nSubject: =?utf-8?q?caf=C3=A9?=\nX-Custom: =?utf-8?b?w6k=?=\n\nbody\n"))
		if m.Header("Subject") != "café" || m.Header("X-Custom") != "é" {
			t.Errorf("headers = %+v", m.Headers)
		}
	})
}

func TestNoHeaderErrors(t *testing.T) {
	for _, in := range []string{"", "\r\n", "Just text, not a message", "\r\nbody after empty header"} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("Parse(%q) should fail", in)
		}
	}
}

// TestNeverPanics mutates sample messages and checks Parse stays well-behaved.
func TestNeverPanics(t *testing.T) {
	samples := sampleMessages()
	for _, s := range samples {
		for cut := 0; cut <= len(s); cut += 3 {
			m, err := Parse(s[:cut])
			if err == nil && m == nil {
				t.Fatalf("nil message without error")
			}
		}
		// Flip bytes at a stride to produce garbage.
		b := append([]byte(nil), s...)
		for i := 0; i < len(b); i += 11 {
			b[i] ^= 0x5a
			Parse(b)
		}
	}
}

func sampleMessages() [][]byte {
	return [][]byte{
		crlf("From: a@example.com\nSubject: =?utf-8?q?x?=\nContent-Type: multipart/mixed; boundary=b\n\n--b\nContent-Type: multipart/alternative; boundary=c\n\n--c\nContent-Type: text/plain; format=flowed; delsp=yes\n\nflow \ned\n--c\nContent-Type: multipart/related; boundary=d\n\n--d\nContent-Type: text/html; charset=iso-8859-1\nContent-Transfer-Encoding: quoted-printable\n\n<p>caf=E9</p>\n--d\nContent-Type: image/png\nContent-ID: <i@x>\nContent-Transfer-Encoding: base64\n\niVBORw0KGgo=\n--d--\n--c--\n--b\nContent-Type: message/rfc822\n\nSubject: inner\n\nx\n--b\nContent-Type: application/pdf; name*0*=UTF-8''a%20b; name*1=.pdf\nContent-Disposition: attachment\n\nx\n--b--\n"),
		crlf("From: \"Broken <a@b\nTo: undisclosed-recipients:;\nDate: garbage\nContent-Type: multipart/signed; boundary=\"s\"\n\n--s\nContent-Type: text/plain; charset=shift_jis\n\n\x82\xa0\n--s\nContent-Type: application/pgp-signature\n\nsig\n--s--\n"),
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range sampleMessages() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		m, err := Parse(raw)
		if err == nil {
			if m == nil {
				t.Fatal("nil message")
			}
			for i, a := range m.Attachments {
				if a.Index != i+1 || a.Size != len(a.Data) {
					t.Fatalf("bad attachment %+v", a)
				}
			}
		}
	})
}

func TestLargeAndPathological(t *testing.T) {
	// A 6 MB base64 attachment.
	big := bytes.Repeat([]byte("0123456789abcdef"), 6<<16)
	raw := crlf("From: a@example.com\nSubject: big\nContent-Type: multipart/mixed; boundary=b\n\n--b\nContent-Type: text/plain\n\nsee attached\n--b\nContent-Type: application/octet-stream; name=big.bin\nContent-Transfer-Encoding: base64\n\n" +
		wrap76(base64.StdEncoding.EncodeToString(big)) + "\n--b--\n")
	m := mustParse(t, raw)
	if len(m.Attachments) != 1 || !bytes.Equal(m.Attachments[0].Data, big) {
		t.Fatalf("big attachment not decoded intact")
	}

	// A broken multipart whose body is 200k delimiter-shaped lines must not
	// make boundary sniffing quadratic.
	var b strings.Builder
	b.WriteString("From: a@example.com\r\nSubject: x\r\nContent-Type: multipart/mixed\r\n\r\n")
	for i := 0; i < 200000; i++ {
		b.WriteString("--x")
		b.WriteString(strings.Repeat("y", i%50))
		b.WriteString("z\r\n")
	}
	start := time.Now()
	mustParse(t, []byte(b.String()))
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("pathological multipart took %v", d)
	}

	// Deeply nested multiparts are bounded.
	var nested strings.Builder
	nested.WriteString("From: a@example.com\r\nSubject: nest\r\nContent-Type: multipart/mixed; boundary=b0\r\n\r\n")
	for i := 1; i <= 100; i++ {
		nested.WriteString("--b" + strconv.Itoa(i-1) + "\r\nContent-Type: multipart/mixed; boundary=b" + strconv.Itoa(i) + "\r\n\r\n")
	}
	nested.WriteString("--b100\r\nContent-Type: text/plain\r\n\r\ndeep\r\n")
	mustParse(t, []byte(nested.String()))
}
