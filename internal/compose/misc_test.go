package compose

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gomail/internal/model"
)

func TestParseAddressList(t *testing.T) {
	cases := []struct {
		in   string
		want []model.Address
	}{
		{"a@b.com", []model.Address{{Email: "a@b.com"}}},
		{"  Name <a@b.com>  ", []model.Address{{Name: "Name", Email: "a@b.com"}}},
		{`"Last, First" <a@b.com>, c@d.com`, []model.Address{{Name: "Last, First", Email: "a@b.com"}, {Email: "c@d.com"}}},
		{"a@b.com; c@d.com,,  ; ", []model.Address{{Email: "a@b.com"}, {Email: "c@d.com"}}},
		{"a@b.com\nc@d.com", []model.Address{{Email: "a@b.com"}, {Email: "c@d.com"}}},
		{"John Q. Public <john@x.com>", []model.Address{{Name: "John Q. Public", Email: "john@x.com"}}},
		{"bob@x.com <bob@x.com>", []model.Address{{Name: "bob@x.com", Email: "bob@x.com"}}},
		{"José Müller <jose@x.com>", []model.Address{{Name: "José Müller", Email: "jose@x.com"}}},
		{"=?utf-8?q?Jos=C3=A9?= <jose@x.com>", []model.Address{{Name: "José", Email: "jose@x.com"}}},
		{`"Smith; Jane" <jane@x.com>; <bare@x.com>`, []model.Address{{Name: "Smith; Jane", Email: "jane@x.com"}, {Email: "bare@x.com"}}},
		{`O'Brien, Pat (work, home) <pat@x.com>`, nil}, // unquoted comma splits the name off
		{"", nil},
		{" , ; ", nil},
	}
	for _, c := range cases {
		got, err := ParseAddressList(c.in)
		if c.want == nil && strings.Contains(c.in, "@") {
			if err == nil {
				t.Errorf("ParseAddressList(%q) = %v, want an error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseAddressList(%q): %v", c.in, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("ParseAddressList(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("ParseAddressList(%q)[%d] = %+v, want %+v", c.in, i, got[i], c.want[i])
			}
		}
	}

	for _, bad := range []string{"not-an-email", "a@b.com, nope", "Name <>", "a@b.com>"} {
		_, err := ParseAddressList(bad)
		if err == nil {
			t.Errorf("ParseAddressList(%q) should fail", bad)
			continue
		}
		if bad == "a@b.com, nope" && !strings.Contains(err.Error(), `"nope"`) {
			t.Errorf("error should name the offending entry: %v", err)
		}
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	pdf := filepath.Join(dir, "Report Q3.PDF")
	os.WriteFile(pdf, []byte("%PDF-1.7"), 0o600)
	f, err := LoadFile(pdf)
	if err != nil {
		t.Fatal(err)
	}
	if f.Filename != "Report Q3.PDF" || f.ContentType != "application/pdf" || string(f.Data) != "%PDF-1.7" {
		t.Errorf("LoadFile = %+v", f)
	}

	unknown := filepath.Join(dir, "data.zzzunknown")
	os.WriteFile(unknown, []byte("plain words"), 0o600)
	if f, err := LoadFile(unknown); err != nil || !strings.HasPrefix(f.ContentType, "text/plain") {
		t.Errorf("sniffed type = %q (%v)", f.ContentType, err)
	}
	png := filepath.Join(dir, "noext")
	os.WriteFile(png, []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), 0o600)
	if f, err := LoadFile(png); err != nil || f.ContentType != "image/png" {
		t.Errorf("sniffed PNG = %q (%v)", f.ContentType, err)
	}

	if _, err := LoadFile(filepath.Join(dir, "missing.txt")); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing file: %v", err)
	}
	if _, err := LoadFile(dir); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("directory: %v", err)
	}
}

func TestPrepareForSend(t *testing.T) {
	fixed := time.Date(2026, 10, 2, 13, 0, 0, 0, time.UTC)
	defer func(f func() time.Time) { now = f }(now)
	now = func() time.Time { return fixed }

	draft := mustBuild(t, Options{
		From: alice, To: []model.Address{bob}, Cc: []model.Address{carol, {Email: "BOB@example.com"}},
		Bcc: []model.Address{dave}, Subject: "draft", Text: "body text\n.\nwith a dot line",
		Attachments: []File{{Filename: "a.bin", Data: []byte{0, 1, 2}}},
		Date:        sendDate, IncludeBcc: true,
	})
	out, from, rcpts, err := PrepareForSend(draft.Raw)
	if err != nil {
		t.Fatal(err)
	}
	if from != "alice@example.com" {
		t.Errorf("from = %q", from)
	}
	if got := strings.Join(rcpts, ","); got != "bob@example.com,carol@example.com,dave@example.org" {
		t.Errorf("rcpts = %q", got)
	}
	if bytes.Contains(out, []byte("Bcc:")) || bytes.Contains(out, []byte("dave@")) {
		t.Errorf("Bcc not stripped:\n%s", out)
	}
	sep := []byte("\r\n\r\n")
	if !bytes.Equal(out[bytes.Index(out, sep):], draft.Raw[bytes.Index(draft.Raw, sep):]) {
		t.Errorf("body changed")
	}
	_, h := parse(t, out)
	if d, _ := h.Date(); !d.Equal(fixed) {
		t.Errorf("Date = %v, want %v", d, fixed)
	}
	if len(h.Values("Date")) != 1 {
		t.Errorf("Date fields = %v", h.Values("Date"))
	}
	if id, _ := h.MessageID(); id != draft.MessageID {
		t.Errorf("Message-ID = %q, want %q", id, draft.MessageID)
	}
	if s, _ := h.Subject(); s != "draft" {
		t.Errorf("Subject = %q", s)
	}

	// LF line endings, no Message-ID, encoded names, a group and two To fields.
	raw := "From: =?utf-8?q?Jos=C3=A9?= <jose@example.com>\n" +
		"To: undisclosed-recipients:;\n" +
		"To: x@example.com\n" +
		"Bcc: Hidden <hidden@example.com>,\n y@example.com\n" +
		"Date: Mon, 1 Jan 2024 00:00:00 +0000\n" +
		"Subject: hi\n\nline one\nline two\n"
	out, from, rcpts, err = PrepareForSend([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if from != "jose@example.com" || strings.Join(rcpts, ",") != "x@example.com,hidden@example.com,y@example.com" {
		t.Errorf("from = %q rcpts = %v", from, rcpts)
	}
	s := string(out)
	if strings.Contains(s, "\r\n") {
		t.Errorf("line endings changed:\n%q", s)
	}
	if !strings.HasSuffix(s, "\n\nline one\nline two\n") || strings.Contains(s, "hidden") || strings.Contains(s, "2024") {
		t.Errorf("output:\n%s", s)
	}
	if !strings.Contains(s, "Message-ID: <") || !strings.Contains(s, "@example.com>\n") {
		t.Errorf("Message-ID not added:\n%s", s)
	}

	for _, bad := range []string{
		"To: a@b.com\n\nbody",
		"From: a@b.com\nSubject: x\n\nbody",
	} {
		if _, _, _, err := PrepareForSend([]byte(bad)); err == nil {
			t.Errorf("PrepareForSend(%q) should fail", bad)
		}
	}
}
