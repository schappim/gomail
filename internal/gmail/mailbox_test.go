package gmail

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"gomail/internal/model"
)

func TestLoginFailureIsHelpful(t *testing.T) {
	f := newFakeServer(t, step{
		cmd:  `LOGIN "me@gmail.com" "wrongwrongwrongw"`,
		resp: []string{"NO [AUTHENTICATIONFAILED] Invalid credentials (Failure)"},
	})
	_, err := f.dial(Config{Username: testUser, Password: "wrongwrongwrongw"})
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
	for _, want := range []string{"IMAP login failed for me@gmail.com", "https://myaccount.google.com/apppasswords",
		"2-Step Verification", "IMAP is enabled", "[AUTHENTICATIONFAILED] Invalid credentials"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestDebugTraceRedactsPassword(t *testing.T) {
	var trace bytes.Buffer
	// The spaced form Google displays is accepted and sent without spaces.
	spaced := "abcd efgh ijkl mnop"
	f := newFakeServer(t, loginStep(), listStep(gmailList))
	c, err := f.dial(Config{Username: testUser, Password: spaced, Debug: &trace})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ResolveMailbox("inbox"); err != nil {
		t.Fatal(err)
	}
	c.Close()

	out := trace.String()
	for _, secret := range []string{testPassword, spaced, "abcd"} {
		if strings.Contains(out, secret) {
			t.Errorf("trace contains the password (%q):\n%s", secret, out)
		}
	}
	for _, want := range []string{"S: * OK [CAPABILITY", " LOGIN [redacted]", `C: `, `LIST "" "*"`, `S: * LIST (\HasNoChildren) "/" "INBOX"`, "LOGOUT"} {
		if !strings.Contains(out, want) {
			t.Errorf("trace lacks %q:\n%s", want, out)
		}
	}
}

func TestLabelsRolesAndNames(t *testing.T) {
	c, _ := newGmail(t, listStep(gmailList))
	labels, err := c.Labels(false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]model.Label{}
	for _, l := range labels {
		got[l.Name] = l
	}
	roles := map[string]string{
		"INBOX":             model.RoleInbox,
		"[Gmail]/All Mail":  model.RoleAll,
		"[Gmail]/Drafts":    model.RoleDrafts,
		"[Gmail]/Important": model.RoleImportant,
		"[Gmail]/Sent Mail": model.RoleSent,
		"[Gmail]/Spam":      model.RoleSpam,
		"[Gmail]/Starred":   model.RoleStarred,
		"[Gmail]/Trash":     model.RoleTrash,
		"Café":              "",
		"Clients/Acme":      "",
	}
	for name, role := range roles {
		l, ok := got[name]
		if !ok {
			t.Errorf("label %q missing", name)
			continue
		}
		if l.Role != role || !l.Selectable || l.Messages != nil {
			t.Errorf("%q: role=%q selectable=%v messages=%v", name, l.Role, l.Selectable, l.Messages)
		}
	}
	if l := got["[Gmail]"]; l.Selectable || l.Role != "" {
		t.Errorf("[Gmail] should be a non-selectable container: %+v", l)
	}
	if want := []string{`\All`, `\HasNoChildren`}; !reflect.DeepEqual(got["[Gmail]/All Mail"].Attributes, want) {
		t.Errorf("attributes = %v", got["[Gmail]/All Mail"].Attributes)
	}
}

func TestLabelsWithCountsUsesListStatus(t *testing.T) {
	c, _ := newGmail(t, step{
		cmd: `LIST "" "*" RETURN (SPECIAL-USE STATUS (MESSAGES UNSEEN))`,
		resp: []string{
			`* LIST (\HasNoChildren) "/" "INBOX"`,
			`* STATUS "INBOX" (MESSAGES 1204 UNSEEN 3)`,
			`* LIST (\HasChildren \Noselect) "/" "[Gmail]"`,
			`* LIST (\All \HasNoChildren) "/" "[Gmail]/All Mail"`,
			`* STATUS "[Gmail]/All Mail" (MESSAGES 52000 UNSEEN 40)`,
			`* LIST (\HasNoChildren) "/" "Caf&AOk-"`,
			`* STATUS "Caf&AOk-" (MESSAGES 7 UNSEEN 0)`,
			`* LIST (\HasNoChildren) "/" "Receipts"`,
		},
	}, step{
		// Not covered by LIST-STATUS: falls back to STATUS.
		cmd:  `STATUS "Receipts" (MESSAGES UNSEEN)`,
		resp: []string{`* STATUS "Receipts" (MESSAGES 12 UNSEEN 1)`},
	})
	labels, err := c.Labels(true)
	if err != nil {
		t.Fatal(err)
	}
	type counts struct{ messages, unread uint32 }
	got := map[string]counts{}
	for _, l := range labels {
		if l.Messages != nil && l.Unread != nil {
			got[l.Name] = counts{*l.Messages, *l.Unread}
		}
	}
	want := map[string]counts{"INBOX": {1204, 3}, "[Gmail]/All Mail": {52000, 40}, "Café": {7, 0}, "Receipts": {12, 1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("counts = %v, want %v", got, want)
	}
	// Roles must survive the extended LIST (Gmail only sends special-use
	// attributes there when RETURN (SPECIAL-USE) is requested).
	roles := map[string]string{}
	for _, l := range labels {
		roles[l.Name] = l.Role
	}
	if roles["INBOX"] != "inbox" || roles["[Gmail]/All Mail"] != "all" {
		t.Errorf("roles = %v", roles)
	}
}

func TestResolveMailbox(t *testing.T) {
	c, _ := newGmail(t, listStep(gmailList))
	cases := map[string]string{
		"inbox":        "INBOX",
		"INBOX":        "INBOX",
		"sent":         "[Gmail]/Sent Mail",
		"Sent-Mail":    "[Gmail]/Sent Mail",
		"drafts":       "[Gmail]/Drafts",
		"draft":        "[Gmail]/Drafts",
		"all":          "[Gmail]/All Mail",
		"all-mail":     "[Gmail]/All Mail",
		"archive":      "[Gmail]/All Mail",
		"Archive":      "Archive", // an exact user label name beats the alias
		"trash":        "[Gmail]/Trash",
		"bin":          "[Gmail]/Trash",
		"spam":         "[Gmail]/Spam",
		"junk":         "[Gmail]/Spam",
		"starred":      "[Gmail]/Starred",
		"flagged":      "[Gmail]/Starred",
		"important":    "[Gmail]/Important",
		"clients/acme": "Clients/Acme",
		"CAFÉ":         "Café",
		"[gmail]/spam": "[Gmail]/Spam",
	}
	for in, want := range cases {
		got, err := c.ResolveMailbox(in)
		if err != nil || got != want {
			t.Errorf("ResolveMailbox(%q) = %q, %v; want %q", in, got, err, want)
		}
	}

	_, err := c.ResolveMailbox("acme")
	if err == nil || !strings.Contains(err.Error(), `"Clients/Acme"`) || !strings.Contains(err.Error(), "did you mean") {
		t.Errorf("unknown mailbox error = %v", err)
	}
	if _, err := c.ResolveMailbox("zzz"); err == nil || !strings.Contains(err.Error(), `"zzz" not found`) {
		t.Errorf("unknown mailbox error = %v", err)
	}
}

func TestGoogleMailAccountAndAppendDraft(t *testing.T) {
	raw := "From: me@gmail.com\r\nTo: bob@example.com\r\nSubject: Hallo\r\nMessage-ID: <draft-1@gomail>\r\n\r\nHi Bob\r\n"
	c, _ := newGmail(t,
		listStep(googleMailList),
		step{
			cmd:  `APPEND "[Google Mail]/Entw&APw-rfe" (\Draft \Seen) ` + lit(raw),
			resp: []string{"OK [APPENDUID 11 31] (Success)"},
		},
		selectStep("EXAMINE", `"[Google Mail]/Entw&APw-rfe"`, 4),
		step{
			cmd: "UID FETCH 31 " + fetchSummaryItems,
			resp: []string{fetchLine(4, 31, 1278455344230334865, 1278455344230334865, `"\\Draft"`, `\Seen \Draft`,
				`("Fri, 2 Oct 2026 10:00:00 +0200" "Hallo" ((NIL NIL "me" "gmail.com")) ((NIL NIL "me" "gmail.com")) ((NIL NIL "me" "gmail.com")) ((NIL NIL "bob" "example.com")) NIL NIL NIL "<draft-1@gomail>")`,
				bsPlain, "")},
		},
	)
	for alias, want := range map[string]string{
		"sent": "[Google Mail]/Gesendet", "drafts": "[Google Mail]/Entwürfe", "all": "[Google Mail]/Alle Nachrichten",
		"trash": "[Google Mail]/Papierkorb", "starred": "[Google Mail]/Markiert", "important": "[Google Mail]/Wichtig",
	} {
		if got, err := c.ResolveMailbox(alias); err != nil || got != want {
			t.Errorf("ResolveMailbox(%q) = %q, %v; want %q", alias, got, err, want)
		}
	}

	s, err := c.AppendDraft([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "11bdfc5cae0c8191" || s.UID != 31 || s.Mailbox != "[Google Mail]/Entwürfe" || s.MessageID != "draft-1@gomail" {
		t.Errorf("draft summary = %+v", s)
	}
	if !reflect.DeepEqual(s.Labels, []string{"DRAFT"}) || s.Unread {
		t.Errorf("labels = %v unread = %v", s.Labels, s.Unread)
	}
}

func TestHiddenAllMailExplainsShowInIMAP(t *testing.T) {
	var list []string
	for _, l := range gmailList {
		if !strings.Contains(l, `\All`) {
			list = append(list, l)
		}
	}
	c, _ := newGmail(t, listStep(list))
	_, err := c.Search(SearchOptions{})
	if err == nil || !strings.Contains(err.Error(), "Show in IMAP") {
		t.Errorf("err = %v", err)
	}
}

func TestCreateLabelInvalidatesCache(t *testing.T) {
	c, _ := newGmail(t,
		listStep(gmailList),
		step{cmd: `CREATE "Clients/Caf&AOk-"`, resp: []string{"OK Success"}},
		listStep(append(append([]string(nil), gmailList...), `* LIST (\HasNoChildren) "/" "Clients/Caf&AOk-"`)),
	)
	if _, err := c.ResolveMailbox("inbox"); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateLabel("Clients/Café"); err != nil {
		t.Fatal(err)
	}
	if got, err := c.ResolveMailbox("clients/café"); err != nil || got != "Clients/Café" {
		t.Errorf("ResolveMailbox after create = %q, %v", got, err)
	}
}
