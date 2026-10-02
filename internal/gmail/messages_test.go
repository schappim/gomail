package gmail

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const (
	msgIDDec = "1278455344230334865" // 11bdfc5cae0c8191
	msgIDHex = "11bdfc5cae0c8191"
	thrIDDec = "1278455344230334000" // 11bdfc5cae0c7e30
	thrIDHex = "11bdfc5cae0c7e30"
)

func TestFetchRaw(t *testing.T) {
	raw := "From: bob@example.com\r\nSubject: hi\r\nMessage-ID: <m1@x>\r\n\r\nHello\r\n"
	c, _ := newGmail(t,
		listStep(gmailList),
		selectStep("EXAMINE", `"[Gmail]/All Mail"`, 100),
		searchStep("UID SEARCH X-GM-MSGID "+msgIDDec, 77),
		step{
			cmd: "UID FETCH 77 (ENVELOPE FLAGS INTERNALDATE RFC822.SIZE UID BODYSTRUCTURE X-GM-MSGID X-GM-THRID X-GM-LABELS BODY.PEEK[])",
			resp: []string{fetchLine(50, 77, 1278455344230334865, 1278455344230334000, `\Inbox`, ``,
				envelope("Thu, 01 Oct 2026 08:00:00 +0000", "hi", "m1@x"), bsPlain, " BODY[] "+lit(raw))},
		},
	)
	got, s, err := c.FetchRaw("0x" + strings.ToUpper(msgIDHex))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != raw {
		t.Errorf("raw = %q", got)
	}
	if s.ID != msgIDHex || s.ThreadID != thrIDHex || s.UID != 77 || !s.Unread || s.Mailbox != "[Gmail]/All Mail" {
		t.Errorf("summary = %+v", s)
	}
}

func TestFetchRawFallsBackToTrash(t *testing.T) {
	c, _ := newGmail(t,
		listStep(gmailList),
		selectStep("EXAMINE", `"[Gmail]/All Mail"`, 100),
		searchStep("UID SEARCH X-GM-MSGID "+msgIDDec),
		selectStep("EXAMINE", `"[Gmail]/Drafts"`, 2),
		searchStep("UID SEARCH X-GM-MSGID "+msgIDDec),
		selectStep("EXAMINE", `"[Gmail]/Trash"`, 9),
		searchStep("UID SEARCH X-GM-MSGID "+msgIDDec, 4),
		step{
			cmd: "UID FETCH 4 (ENVELOPE FLAGS INTERNALDATE RFC822.SIZE UID BODYSTRUCTURE X-GM-MSGID X-GM-THRID X-GM-LABELS BODY.PEEK[])",
			resp: []string{fetchLine(9, 4, 1278455344230334865, 1, ``, `\Seen`,
				envelope("Thu, 01 Oct 2026 08:00:00 +0000", "gone", "g@x"), bsPlain, " BODY[] "+lit("Subject: gone\r\n\r\nx"))},
		},
	)
	_, s, err := c.FetchRaw(msgIDHex)
	if err != nil {
		t.Fatal(err)
	}
	if s.Mailbox != "[Gmail]/Trash" || !reflect.DeepEqual(s.Labels, []string{"TRASH"}) {
		t.Errorf("summary = %+v", s)
	}
}

func TestFetchRawNotFound(t *testing.T) {
	c, _ := newGmail(t,
		listStep(gmailList),
		selectStep("EXAMINE", `"[Gmail]/All Mail"`, 1),
		searchStep("UID SEARCH X-GM-MSGID 255"),
		selectStep("EXAMINE", `"[Gmail]/Drafts"`, 1),
		searchStep("UID SEARCH X-GM-MSGID 255"),
		selectStep("EXAMINE", `"[Gmail]/Trash"`, 1),
		searchStep("UID SEARCH X-GM-MSGID 255"),
		selectStep("EXAMINE", `"[Gmail]/Spam"`, 1),
		searchStep("UID SEARCH X-GM-MSGID 255"),
	)
	_, _, err := c.FetchRaw("ff")
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "message ff") {
		t.Errorf("err = %v", err)
	}
}

func TestThreadFromMessageID(t *testing.T) {
	c, _ := newGmail(t,
		listStep(gmailList),
		selectStep("EXAMINE", `"[Gmail]/All Mail"`, 100),
		// The id is a message id, not a thread id.
		searchStep("UID SEARCH X-GM-THRID "+msgIDDec),
		searchStep("UID SEARCH X-GM-MSGID "+msgIDDec, 12),
		step{
			cmd:  "UID FETCH 12 (UID X-GM-THRID)",
			resp: []string{"* 40 FETCH (X-GM-THRID " + thrIDDec + " UID 12)"},
		},
		searchStep("UID SEARCH X-GM-THRID "+thrIDDec, 15, 10, 12),
		step{
			cmd: "UID FETCH 10,12,15 (ENVELOPE FLAGS INTERNALDATE RFC822.SIZE UID BODYSTRUCTURE X-GM-MSGID X-GM-THRID X-GM-LABELS BODY.PEEK[])",
			resp: []string{
				fetchLine(41, 15, 3, 1278455344230334000, `\Inbox`, ``, envelope("Thu, 01 Oct 2026 10:00:00 +0000", "Re: plan", "c@x"), bsPlain, " BODY[] "+lit("third")),
				fetchLine(38, 10, 1, 1278455344230334000, `\Sent`, `\Seen`, envelope("Thu, 01 Oct 2026 08:00:00 +0000", "plan", "a@x"), bsPlain, " BODY[] "+lit("first")),
				fetchLine(40, 12, 1278455344230334865, 1278455344230334000, `\Inbox`, `\Seen`, envelope("Thu, 01 Oct 2026 09:00:00 +0000", "Re: plan", "b@x"), bsPlain, " BODY[] "+lit("second")),
			},
		},
	)
	msgs, err := c.Thread(msgIDHex, true)
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, m := range msgs {
		bodies = append(bodies, string(m.Raw))
		if m.Summary.ThreadID != thrIDHex {
			t.Errorf("thread id = %q", m.Summary.ThreadID)
		}
	}
	if !reflect.DeepEqual(bodies, []string{"first", "second", "third"}) {
		t.Errorf("thread order/raw = %v", bodies)
	}
	if !reflect.DeepEqual(msgs[0].Summary.Labels, []string{"SENT"}) {
		t.Errorf("labels = %v", msgs[0].Summary.Labels)
	}
}

func TestThreadNotFound(t *testing.T) {
	c, _ := newGmail(t,
		listStep(gmailList),
		selectStep("EXAMINE", `"[Gmail]/All Mail"`, 100),
		searchStep("UID SEARCH X-GM-THRID 4096"),
		searchStep("UID SEARCH X-GM-MSGID 4096"),
	)
	if _, err := c.Thread("1000", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestAppendDraftLocatesByMessageID(t *testing.T) {
	// Bare LF line endings are converted to CRLF for APPEND.
	raw := "To: bob@example.com\nSubject: plan\nMessage-Id: <CAB+x7=draft@mail.example>\n\nbody\n"
	wire := strings.ReplaceAll(raw, "\n", "\r\n")
	c, _ := newGmail(t,
		listStep(gmailList),
		step{cmd: `APPEND "[Gmail]/Drafts" (\Draft \Seen) ` + lit(wire), resp: []string{"OK (Success)"}},
		selectStep("EXAMINE", `"[Gmail]/Drafts"`, 3),
		// Not indexed yet: both searches miss, then NOOP and retry.
		searchStep(`UID SEARCH X-GM-RAW "rfc822msgid:CAB+x7=draft@mail.example"`),
		searchStep(`UID SEARCH HEADER Message-ID "CAB+x7=draft@mail.example"`),
		step{cmd: "NOOP", resp: []string{"* 4 EXISTS", "OK Success"}},
		searchStep(`UID SEARCH X-GM-RAW "rfc822msgid:CAB+x7=draft@mail.example"`, 40),
		step{
			cmd: "UID FETCH 40 " + fetchSummaryItems,
			resp: []string{fetchLine(4, 40, 99, 98, `\Draft`, `\Seen \Draft`,
				envelope("Fri, 02 Oct 2026 10:00:00 +0000", "plan", "CAB+x7=draft@mail.example"), bsPlain, "")},
		},
	)
	s, err := c.AppendDraft([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if s.UID != 40 || s.ID != "63" || s.MessageID != "CAB+x7=draft@mail.example" || !reflect.DeepEqual(s.Labels, []string{"DRAFT"}) {
		t.Errorf("summary = %+v", s)
	}
}

func TestAppendDraftWithoutMessageIDUsesHighestUID(t *testing.T) {
	raw := "Subject: no id\r\n\r\nx\r\n"
	c, _ := newGmail(t,
		listStep(gmailList),
		step{cmd: `APPEND "[Gmail]/Drafts" (\Draft \Seen) ` + lit(raw), resp: []string{"OK (Success)"}},
		selectStep("EXAMINE", `"[Gmail]/Drafts"`, 3),
		searchStep("UID SEARCH ALL", 3, 41, 7),
		step{
			cmd: "UID FETCH 41 " + fetchSummaryItems,
			resp: []string{fetchLine(3, 41, 5, 5, ``, `\Seen \Draft`,
				`(NIL "no id" NIL NIL NIL NIL NIL NIL NIL NIL)`, bsPlain, "")},
		},
	)
	s, err := c.AppendDraft([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if s.UID != 41 || s.Subject != "no id" {
		t.Errorf("summary = %+v", s)
	}
}

func TestDeleteDraft(t *testing.T) {
	c, f := newGmail(t,
		listStep(gmailList),
		selectStep("SELECT", `"[Gmail]/Drafts"`, 4),
		searchStep("UID SEARCH X-GM-MSGID "+msgIDDec, 31),
		step{cmd: `UID STORE 31 +FLAGS.SILENT (\Deleted)`},
		step{cmd: "UID EXPUNGE 31", resp: []string{"* 3 EXPUNGE", "OK Success"}},
	)
	if err := c.DeleteDraft(msgIDHex); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`LOGIN "me@gmail.com" "abcdefghijklmnop"`,
		"CAPABILITY",
		`LIST "" "*"`,
		`SELECT "[Gmail]/Drafts"`,
		"UID SEARCH X-GM-MSGID " + msgIDDec,
		`UID STORE 31 +FLAGS.SILENT (\Deleted)`,
		"UID EXPUNGE 31",
	}
	if got := f.commands(); !reflect.DeepEqual(got, want) {
		t.Errorf("command sequence:\n got %q\nwant %q", got, want)
	}
}

func TestDeleteDraftRejectsNonDraft(t *testing.T) {
	c, _ := newGmail(t,
		listStep(gmailList),
		selectStep("SELECT", `"[Gmail]/Drafts"`, 4),
		searchStep("UID SEARCH X-GM-MSGID "+msgIDDec),
	)
	err := c.DeleteDraft(msgIDHex)
	if !errors.Is(err, ErrNotDraft) || !strings.Contains(err.Error(), "not a draft") {
		t.Errorf("err = %v", err)
	}
}

func TestModifyLabels(t *testing.T) {
	c, _ := newGmail(t,
		listStep(gmailList),
		selectStep("SELECT", `"[Gmail]/All Mail"`, 100),
		searchStep("UID SEARCH X-GM-MSGID "+msgIDDec, 42),
		step{
			cmd: `UID STORE 42 +X-GM-LABELS (\Inbox \Starred "Clients/Acme" "Caf&AOk-" "Project \"X\"")`,
			// Gmail echoes the new labels; the client must cope.
			resp: []string{`* 7 FETCH (X-GM-LABELS (\Inbox \Starred "Clients/Acme" "Caf&AOk-" "Project \"X\"") UID 42)`, "OK Success"},
		},
		step{cmd: `UID STORE 42 -X-GM-LABELS (\Important "Receipts")`},
	)
	err := c.ModifyLabels([]string{msgIDHex}, []string{"inbox", "Starred", "Clients/Acme", "Café", `Project "X"`}, []string{"IMPORTANT", "Receipts"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestModifyLabelsReportsUnknownIDs(t *testing.T) {
	c, _ := newGmail(t,
		listStep(gmailList),
		selectStep("SELECT", `"[Gmail]/All Mail"`, 100),
		searchStep("UID SEARCH OR X-GM-MSGID 10 OR X-GM-MSGID 11 X-GM-MSGID 12", 9, 5),
		step{
			cmd:  "UID FETCH 5,9 (UID X-GM-MSGID)",
			resp: []string{"* 1 FETCH (X-GM-MSGID 10 UID 5)", "* 2 FETCH (X-GM-MSGID 12 UID 9)"},
		},
	)
	err := c.ModifyLabels([]string{"a", "b", "c"}, []string{"Receipts"}, nil)
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "message b ") {
		t.Errorf("err = %v", err)
	}
	if err := c.ModifyLabels([]string{"a", "nope"}, []string{"x"}, nil); err == nil || !strings.Contains(err.Error(), "invalid Gmail id") {
		t.Errorf("invalid id err = %v", err)
	}
}

func TestFlagsArchiveAndTrash(t *testing.T) {
	c, _ := newGmail(t,
		listStep(gmailList),
		selectStep("SELECT", `"[Gmail]/All Mail"`, 100),
		searchStep("UID SEARCH OR X-GM-MSGID 10 X-GM-MSGID 11", 5, 9),
		step{cmd: "UID FETCH 5,9 (UID X-GM-MSGID)", resp: []string{"* 1 FETCH (X-GM-MSGID 10 UID 5)", "* 2 FETCH (X-GM-MSGID 11 UID 9)"}},
		step{cmd: `UID STORE 5,9 +FLAGS.SILENT (\Seen)`},
		searchStep("UID SEARCH X-GM-MSGID 10", 5),
		step{cmd: `UID STORE 5 -FLAGS.SILENT (\Seen)`},
		searchStep("UID SEARCH X-GM-MSGID 10", 5),
		step{cmd: `UID STORE 5 +FLAGS.SILENT (\Flagged)`},
		searchStep("UID SEARCH X-GM-MSGID 10", 5),
		step{cmd: `UID STORE 5 -FLAGS.SILENT (\Flagged)`},
		searchStep("UID SEARCH X-GM-MSGID 10", 5),
		step{cmd: `UID STORE 5 -X-GM-LABELS (\Inbox)`, resp: []string{`* 1 FETCH (X-GM-LABELS () UID 5)`, "OK Success"}},
		searchStep("UID SEARCH X-GM-MSGID 10", 5),
		step{cmd: `UID MOVE 5 "[Gmail]/Trash"`, resp: []string{"* OK [COPYUID 11 5 88]", "* 1 EXPUNGE", "OK Success"}},
	)
	steps := []func() error{
		func() error { return c.MarkRead([]string{"a", "b"}, true) },
		func() error { return c.MarkRead([]string{"a"}, false) },
		func() error { return c.Star([]string{"a"}, true) },
		func() error { return c.Star([]string{"a"}, false) },
		func() error { return c.Archive([]string{"a"}) },
		func() error { return c.Trash([]string{"a"}) },
	}
	for i, fn := range steps {
		if err := fn(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
}

func TestTrashWithoutMoveFallsBack(t *testing.T) {
	f := newFakeServer(t,
		loginStep(),
		listStep(gmailList),
		selectStep("SELECT", `"[Gmail]/All Mail"`, 100),
		searchStep("UID SEARCH X-GM-MSGID 10", 5),
		step{cmd: `UID COPY 5 "[Gmail]/Trash"`},
		step{cmd: `UID STORE 5 +FLAGS.SILENT (\Deleted)`},
		step{cmd: "UID EXPUNGE 5"},
	)
	f.setCaps(strings.Replace(gmailCaps, " MOVE", "", 1))
	c, err := f.dial(Config{Username: testUser, Password: testPassword})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Trash([]string{"a"}); err != nil {
		t.Fatal(err)
	}
}

func TestFindByMessageID(t *testing.T) {
	c, _ := newGmail(t,
		listStep(gmailList),
		selectStep("EXAMINE", `"[Gmail]/All Mail"`, 100),
		searchStep(`UID SEARCH X-GM-RAW "rfc822msgid:abc@example.com"`, 3, 8),
		step{
			cmd: "UID FETCH 3,8 " + fetchSummaryItems,
			resp: []string{
				fetchLine(1, 3, 30, 30, `\Inbox`, `\Seen`, envelope("Wed, 30 Sep 2026 08:00:00 +0000", "x", "abc@example.com"), bsPlain, ""),
				fetchLine(2, 8, 80, 30, `\Sent`, `\Seen`, envelope("Thu, 01 Oct 2026 08:00:00 +0000", "x", "abc@example.com"), bsPlain, ""),
			},
		},
	)
	s, err := c.FindByMessageID("<abc@example.com>")
	if err != nil {
		t.Fatal(err)
	}
	if s.UID != 8 || s.ID != "50" {
		t.Errorf("want the newest match, got %+v", s)
	}
}

func TestFindByMessageIDFallsBackToHeaderAndDrafts(t *testing.T) {
	c, _ := newGmail(t,
		listStep(gmailList),
		selectStep("EXAMINE", `"[Gmail]/All Mail"`, 100),
		searchStep(`UID SEARCH X-GM-RAW "rfc822msgid:zzz@x"`),
		searchStep(`UID SEARCH HEADER Message-ID "zzz@x"`),
		selectStep("EXAMINE", `"[Gmail]/Drafts"`, 1),
		searchStep(`UID SEARCH X-GM-RAW "rfc822msgid:zzz@x"`),
		searchStep(`UID SEARCH HEADER Message-ID "zzz@x"`),
	)
	if _, err := c.FindByMessageID("zzz@x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}
