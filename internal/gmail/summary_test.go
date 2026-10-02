package gmail

import (
	"bufio"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"

	"gomail/internal/model"
)

// parseFetch parses one untagged FETCH response line the way the client's
// reader would.
func parseFetch(t *testing.T, line string) *imap.Message {
	t.Helper()
	r := imap.NewReader(bufio.NewReader(strings.NewReader(line + "\r\n")))
	resp, err := imap.ReadResp(r)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	msg, ok, err := parseFetchResp(resp)
	if !ok || err != nil {
		t.Fatalf("parseFetchResp: ok=%v err=%v", ok, err)
	}
	return msg
}

var (
	allMail  = newMailbox("[Gmail]/All Mail", []string{`\All`, `\HasNoChildren`})
	inbox    = newMailbox("INBOX", []string{`\HasNoChildren`})
	sentMail = newMailbox("[Gmail]/Sent Mail", []string{`\HasNoChildren`, `\Sent`})
	acme     = newMailbox("Clients/Acme", []string{`\HasNoChildren`})
	starred  = newMailbox("[Gmail]/Starred", []string{`\Flagged`, `\HasNoChildren`})
)

func TestSummaryFromGmailFetch(t *testing.T) {
	line := `* 4012 FETCH (X-GM-THRID 1278455344230334000 X-GM-MSGID 1278455344230334865 ` +
		`X-GM-LABELS ("\\Important" \Inbox "Clients/Acme" "Project X" "&AOk-t&AOk-" Receipts) UID 98765 ` +
		`RFC822.SIZE 48213 INTERNALDATE "01-Oct-2026 09:30:12 +0000" FLAGS (\Flagged $NotPhishing) ` +
		`ENVELOPE ("Thu, 1 Oct 2026 19:30:00 +1000" "=?ISO-8859-1?Q?Caf=E9_meeting?=" ` +
		`(("=?UTF-8?B?QsOpYSBTbWl0aA==?=" NIL "bea" "example.com")) (("Bea" NIL "bea" "example.com")) ` +
		`(("Bea" NIL "bea" "example.com")) ((NIL NIL "me" "gmail.com")("Ann" NIL "ann" "example.org")) ` +
		`(("Team" NIL "team" "example.org")) NIL "<parent@example.com>" "<CAF=abc123@mail.gmail.com>") ` +
		`BODYSTRUCTURE ` + bsPDF + `)`

	s := newSummary(parseFetch(t, line), allMail)

	want := model.MessageSummary{
		ID:        "11bdfc5cae0c8191",
		ThreadID:  "11bdfc5cae0c7e30",
		MessageID: "CAF=abc123@mail.gmail.com",
		InReplyTo: "parent@example.com",
		Date:      time.Date(2026, 10, 1, 19, 30, 0, 0, time.FixedZone("", 10*3600)),
		From:      []model.Address{{Name: "Béa Smith", Email: "bea@example.com"}},
		To:        []model.Address{{Email: "me@gmail.com"}, {Name: "Ann", Email: "ann@example.org"}},
		Cc:        []model.Address{{Name: "Team", Email: "team@example.org"}},
		ReplyTo:   []model.Address{{Name: "Bea", Email: "bea@example.com"}},
		Subject:   "Café meeting",
		Labels:    []string{"Clients/Acme", "IMPORTANT", "INBOX", "Project X", "Receipts", "STARRED", "été"},
		Flags:     []string{`\Flagged`, "$notphishing"},
		Unread:    true,
		Starred:   true,
		Size:      48213,
		HasAttach: true,
		Mailbox:   "[Gmail]/All Mail",
		UID:       98765,
	}
	if !s.Date.Equal(want.Date) {
		t.Errorf("Date = %v, want %v", s.Date, want.Date)
	}
	s.Date = want.Date
	if !reflect.DeepEqual(s, want) {
		t.Errorf("summary mismatch\n got: %+v\nwant: %+v", s, want)
	}
}

func TestSummaryDateFallsBackToInternalDate(t *testing.T) {
	line := `* 1 FETCH (X-GM-THRID 5 X-GM-MSGID 6 X-GM-LABELS () UID 7 RFC822.SIZE 10 ` +
		`INTERNALDATE "02-Oct-2026 08:00:00 +0200" FLAGS (\Seen) ` +
		`ENVELOPE (NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL) BODYSTRUCTURE ` + bsPlain + `)`
	s := newSummary(parseFetch(t, line), inbox)
	if want := time.Date(2026, 10, 2, 6, 0, 0, 0, time.UTC); !s.Date.Equal(want) {
		t.Errorf("Date = %v, want %v", s.Date, want)
	}
	if s.Unread || s.Starred || s.HasAttach {
		t.Errorf("flags: unread=%v starred=%v attach=%v", s.Unread, s.Starred, s.HasAttach)
	}
	if s.ID != "6" || s.ThreadID != "5" {
		t.Errorf("ids = %q/%q", s.ID, s.ThreadID)
	}
	if !reflect.DeepEqual(s.Labels, []string{"INBOX"}) || s.From == nil {
		t.Errorf("labels = %#v, from = %#v", s.Labels, s.From)
	}
}

func TestNormalizeLabels(t *testing.T) {
	raw := []string{`\Inbox`, `\Sent`, `\Draft`, `\Important`, `\Starred`, `\Trash`, `\Spam`, `\Muted`,
		"Clients/Acme", "Caf&AOk-", "Bad&-Name", "My Label", `\Inbox`}
	got := normalizeLabels(raw)
	want := []string{"Bad&Name", "Café", "Clients/Acme", "DRAFT", "IMPORTANT", "INBOX", "MUTED", "My Label", "SENT", "SPAM", "STARRED", "TRASH"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("normalizeLabels =\n %#v\nwant\n %#v", got, want)
	}
	if got := normalizeLabels(nil); got == nil || len(got) != 0 {
		t.Errorf("empty input should give an empty, non-nil list: %#v", got)
	}
}

func TestSelectedMailboxLabelAddedBack(t *testing.T) {
	// Gmail omits the selected mailbox's own label from X-GM-LABELS.
	line := `* 3 FETCH (X-GM-THRID 9 X-GM-MSGID 9 X-GM-LABELS ("\\Important") UID 3 RFC822.SIZE 1 ` +
		`INTERNALDATE "01-Oct-2026 09:30:00 +0000" FLAGS (\Seen) ` +
		`ENVELOPE (NIL "x" NIL NIL NIL NIL NIL NIL NIL NIL) BODYSTRUCTURE ` + bsPlain + `)`
	msg := parseFetch(t, line)

	cases := []struct {
		mb   *mailbox
		want []string
	}{
		{allMail, []string{"IMPORTANT"}},
		{inbox, []string{"IMPORTANT", "INBOX"}},
		{sentMail, []string{"IMPORTANT", "SENT"}},
		{acme, []string{"Clients/Acme", "IMPORTANT"}},
		{starred, []string{"IMPORTANT", "STARRED"}},
		{newMailbox("[Gmail]/Drafts", []string{`\Drafts`}), []string{"DRAFT", "IMPORTANT"}},
		{newMailbox("[Gmail]/Important", []string{`\Important`}), []string{"IMPORTANT"}},
		{newMailbox("Caf&AOk-", nil), []string{"Caf&AOk-", "IMPORTANT"}}, // name is already decoded in practice
	}
	for _, tc := range cases {
		if got := newSummary(msg, tc.mb).Labels; !reflect.DeepEqual(got, tc.want) {
			t.Errorf("in %q: labels = %#v, want %#v", tc.mb.name, got, tc.want)
		}
	}
}

func TestGmailExtensionItemParsing(t *testing.T) {
	msg := parseFetch(t, `* 1 FETCH (X-GM-MSGID 18446744073709551615 X-GM-THRID 4294967296 UID 1 `+
		`X-GM-LABELS (\Inbox "\\Important" "With Space" "&BB8EQAQ4BDIENQRC-" "quote \"q\"" "back\\slash"))`)
	if v, err := gmNumber(msg.Items[itemMsgID]); err != nil || v != ^uint64(0) {
		t.Errorf("X-GM-MSGID = %d, %v", v, err)
	}
	if v, err := gmNumber(msg.Items[itemThrID]); err != nil || v != 1<<32 {
		t.Errorf("X-GM-THRID = %d, %v", v, err)
	}
	raw := gmLabels(msg.Items[itemLabels])
	wantRaw := []string{`\Inbox`, `\Important`, "With Space", "&BB8EQAQ4BDIENQRC-", `quote "q"`, `back\slash`}
	if !reflect.DeepEqual(raw, wantRaw) {
		t.Errorf("raw labels = %#v, want %#v", raw, wantRaw)
	}
	got := normalizeLabels(raw)
	want := []string{"IMPORTANT", "INBOX", "With Space", `back\slash`, `quote "q"`, "Привет"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("labels = %#v, want %#v", got, want)
	}
}

func bodyStructure(t *testing.T, bs string) *imap.BodyStructure {
	t.Helper()
	msg := parseFetch(t, `* 1 FETCH (UID 1 BODYSTRUCTURE `+bs+`)`)
	if msg.BodyStructure == nil {
		t.Fatalf("no body structure parsed from %s", bs)
	}
	return msg.BodyStructure
}

func TestHasAttachment(t *testing.T) {
	cases := []struct {
		name string
		bs   string
		want bool
	}{
		{"plain text", bsPlain, false},
		{"alternative", bsAlt, false},
		{"pdf attachment", bsPDF, true},
		{"inline logo with content-id", bsLogo, false},
		{"related image without disposition",
			`(("TEXT" "HTML" ("CHARSET" "UTF-8") NIL NIL "7BIT" 300 8 NIL NIL NIL)("IMAGE" "JPEG" ("NAME" "photo.jpg") "<img1@x>" NIL "BASE64" 9000 NIL NIL NIL) "RELATED" ("BOUNDARY" "r") NIL NIL)`,
			false},
		{"image with filename but no content-id in mixed",
			`(("TEXT" "PLAIN" ("CHARSET" "UTF-8") NIL NIL "7BIT" 10 1 NIL NIL NIL)("IMAGE" "JPEG" ("NAME" "photo.jpg") NIL NIL "BASE64" 9000 NIL ("INLINE" ("FILENAME" "photo.jpg")) NIL) "MIXED" ("BOUNDARY" "m") NIL NIL)`,
			true},
		{"attachment disposition without filename",
			`(("TEXT" "PLAIN" ("CHARSET" "UTF-8") NIL NIL "7BIT" 10 1 NIL NIL NIL)("APPLICATION" "OCTET-STREAM" NIL NIL NIL "BASE64" 90 NIL ("ATTACHMENT" NIL) NIL) "MIXED" ("BOUNDARY" "m") NIL NIL)`,
			true},
		{"RFC 2231 filename only",
			`(("TEXT" "PLAIN" ("CHARSET" "UTF-8") NIL NIL "7BIT" 10 1 NIL NIL NIL)("APPLICATION" "PDF" NIL NIL NIL "BASE64" 90 NIL ("INLINE" ("FILENAME*" "utf-8''r%C3%A9sum%C3%A9.pdf")) NIL) "MIXED" ("BOUNDARY" "m") NIL NIL)`,
			true},
		{"single-part pdf",
			`("APPLICATION" "PDF" ("NAME" "scan.pdf") NIL NIL "BASE64" 90 NIL ("ATTACHMENT" ("FILENAME" "scan.pdf")) NIL)`,
			true},
		{"forwarded message",
			`(("TEXT" "PLAIN" ("CHARSET" "UTF-8") NIL NIL "7BIT" 10 1 NIL NIL NIL)("MESSAGE" "RFC822" NIL NIL NIL "7BIT" 500 (NIL "fwd" NIL NIL NIL NIL NIL NIL NIL NIL) ("TEXT" "PLAIN" NIL NIL NIL "7BIT" 5 1 NIL NIL NIL) 12 NIL NIL NIL) "MIXED" ("BOUNDARY" "m") NIL NIL)`,
			true},
	}
	for _, tc := range cases {
		if got := hasAttachment(bodyStructure(t, tc.bs)); got != tc.want {
			t.Errorf("%s: hasAttachment = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSnippetPart(t *testing.T) {
	cases := []struct {
		name, bs, path, mediaType, encoding string
	}{
		{"single part", bsPlain, "1", "text/plain", "7bit"},
		{"html only", bsHTML, "1", "text/html", "quoted-printable"},
		{"alternative", bsAlt, "1", "text/plain", "7bit"},
		{"nested", bsPDF, "1.1", "text/plain", "7bit"},
		{"html first in related",
			`((("TEXT" "HTML" ("CHARSET" "UTF-8") NIL NIL "BASE64" 300 8 NIL NIL NIL)("IMAGE" "PNG" ("NAME" "a.png") "<a>" NIL "BASE64" 9 NIL NIL NIL) "RELATED" ("BOUNDARY" "r") NIL NIL)("TEXT" "PLAIN" ("CHARSET" "UTF-8" "NAME" "notes.txt") NIL NIL "7BIT" 10 1 NIL ("ATTACHMENT" ("FILENAME" "notes.txt")) NIL) "MIXED" ("BOUNDARY" "m") NIL NIL)`,
			"1.1", "text/html", "base64"},
	}
	for _, tc := range cases {
		tp, ok := snippetPart(bodyStructure(t, tc.bs))
		if !ok || sectionPath(tp.path) != tc.path || tp.mediaType != tc.mediaType || tp.encoding != tc.encoding || tp.charset != "UTF-8" {
			t.Errorf("%s: got %+v ok=%v, want path %s %s %s", tc.name, tp, ok, tc.path, tc.mediaType, tc.encoding)
		}
	}
	if _, ok := snippetPart(bodyStructure(t, `("IMAGE" "PNG" NIL NIL NIL "BASE64" 9 NIL NIL NIL)`)); ok {
		t.Error("an image-only message has no snippet part")
	}
}

func TestStripAngles(t *testing.T) {
	for in, want := range map[string]string{
		"<a@b>":         "a@b",
		" <a@b> ":       "a@b",
		"a@b":           "a@b",
		"<a@b> <c@d>":   "a@b",
		"":              "",
		"<unterminated": "unterminated",
	} {
		if got := stripAngles(in); got != want {
			t.Errorf("stripAngles(%q) = %q, want %q", in, got, want)
		}
	}
}
