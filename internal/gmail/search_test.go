package gmail

import (
	"reflect"
	"testing"

	"gomail/internal/mailparse"
)

func uidsOf(r *SearchResult) []uint32 {
	var out []uint32
	for _, m := range r.Messages {
		out = append(out, m.UID)
	}
	return out
}

func TestSearchWithASCIIQuery(t *testing.T) {
	c, _ := newGmail(t,
		listStep(gmailList),
		selectStep("EXAMINE", `"[Gmail]/All Mail"`, 5000),
		searchStep(`UID SEARCH X-GM-RAW "from:bob subject:\"quarterly report\" has:attachment"`, 412, 101, 333, 205),
		step{
			// Total 4, offset 1, limit 2: the second and third newest UIDs.
			cmd: "UID FETCH 205,333 " + fetchSummaryItems,
			resp: []string{
				fetchLine(10, 205, 1001, 1001, `\Inbox`, `\Seen`, envelope("Wed, 30 Sep 2026 12:00:00 +0000", "Q3 report", "a@x"), bsPDF, ""),
				// An unsolicited flag update in the middle must be ignored.
				`* 3 FETCH (FLAGS (\Seen))`,
				fetchLine(11, 333, 1002, 1001, `"\\Important"`, ``, envelope("Tue, 29 Sep 2026 12:00:00 +0000", "Re: Q3 report", "b@x"), bsPlain, ""),
			},
		},
	)
	res, err := c.Search(SearchOptions{Query: `from:bob subject:"quarterly report" has:attachment`, Limit: 2, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 4 || res.Mailbox != "[Gmail]/All Mail" {
		t.Errorf("total=%d mailbox=%q", res.Total, res.Mailbox)
	}
	// Date order wins over UID order.
	if got := uidsOf(res); !reflect.DeepEqual(got, []uint32{205, 333}) {
		t.Fatalf("uids = %v", got)
	}
	first, second := res.Messages[0], res.Messages[1]
	if first.ID != "3e9" || first.Subject != "Q3 report" || !first.HasAttach || first.Unread || first.MessageID != "a@x" {
		t.Errorf("first = %+v", first)
	}
	if !reflect.DeepEqual(first.Labels, []string{"INBOX"}) || !reflect.DeepEqual(second.Labels, []string{"IMPORTANT"}) {
		t.Errorf("labels = %v / %v", first.Labels, second.Labels)
	}
	if second.ThreadID != "3e9" || !second.Unread || second.HasAttach || second.Mailbox != "[Gmail]/All Mail" {
		t.Errorf("second = %+v", second)
	}
}

func TestSearchWithNonASCIIQuery(t *testing.T) {
	c, f := newGmail(t,
		listStep(gmailList),
		selectStep("EXAMINE", "INBOX", 12),
		searchStep("UID SEARCH CHARSET UTF-8 X-GM-RAW "+lit("subject:café")),
	)
	res, err := c.Search(SearchOptions{Mailbox: "inbox", Query: "subject:café"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 0 || len(res.Messages) != 0 || res.Messages == nil || res.Mailbox != "INBOX" {
		t.Errorf("result = %+v", res)
	}
	// "café" is 5 bytes: the literal length counts bytes, not runes.
	cmds := f.commands()
	if got := cmds[len(cmds)-1]; got != "UID SEARCH CHARSET UTF-8 X-GM-RAW {13}\r\nsubject:café" {
		t.Errorf("wire command = %q", got)
	}
}

func TestSearchWithoutQueryPagesBySequence(t *testing.T) {
	c, _ := newGmail(t,
		listStep(gmailList),
		selectStep("EXAMINE", "INBOX", 120),
		step{
			// EXISTS 120, offset 5, limit 10: sequence numbers 106..115.
			cmd: "FETCH 106:115 " + fetchSummaryItems,
			resp: []string{
				fetchLine(114, 9001, 77, 70, ``, `\Seen`, envelope("Thu, 01 Oct 2026 08:00:00 +0000", "older", "o@x"), bsPlain, ""),
				fetchLine(115, 9002, 78, 70, `\Important`, ``, envelope("Thu, 01 Oct 2026 09:00:00 +0000", "newer", "n@x"), bsAlt, ""),
				`* 121 EXISTS`,
			},
		},
	)
	res, err := c.Search(SearchOptions{Mailbox: "INBOX", Limit: 10, Offset: 5})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 120 {
		t.Errorf("total = %d", res.Total)
	}
	if got := uidsOf(res); !reflect.DeepEqual(got, []uint32{9002, 9001}) {
		t.Fatalf("uids = %v", got)
	}
	if got := res.Messages[0].Labels; !reflect.DeepEqual(got, []string{"IMPORTANT", "INBOX"}) {
		t.Errorf("labels = %v (INBOX must be added back)", got)
	}
}

func TestSearchWithoutQueryPastTheEnd(t *testing.T) {
	c, _ := newGmail(t,
		listStep(gmailList),
		selectStep("EXAMINE", `"Clients/Acme"`, 3),
	)
	res, err := c.Search(SearchOptions{Mailbox: "Clients/Acme", Offset: 3})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 3 || len(res.Messages) != 0 {
		t.Errorf("result = %+v", res)
	}
}

func TestSearchDefaultsAndSnippets(t *testing.T) {
	plain := "Hi Bob, the numbers are attached."
	html := "<p>Hello <b>world</b></p>"
	c, _ := newGmail(t,
		listStep(gmailList),
		selectStep("EXAMINE", `"[Gmail]/All Mail"`, 30),
		step{
			// Limit defaults to 25: sequence numbers 6..30.
			cmd: "FETCH 6:30 " + fetchSummaryItems,
			resp: []string{
				fetchLine(28, 501, 1, 1, ``, `\Seen`, envelope("Thu, 01 Oct 2026 08:00:00 +0000", "a", "a@x"), bsPDF, ""),
				fetchLine(29, 502, 2, 2, ``, `\Seen`, envelope("Thu, 01 Oct 2026 09:00:00 +0000", "b", "b@x"), bsHTML, ""),
				fetchLine(30, 503, 3, 3, ``, `\Seen`, envelope("Thu, 01 Oct 2026 10:00:00 +0000", "c", "c@x"), bsPlain, ""),
			},
		},
		// One FETCH per distinct section path, in path order.
		step{
			cmd:  "UID FETCH 502:503 (UID BODY.PEEK[1]<0.4096>)",
			resp: []string{"* 29 FETCH (UID 502 BODY[1]<0> " + lit(html) + ")", "* 30 FETCH (UID 503 BODY[1]<0> " + lit(plain) + ")"},
		},
		step{
			cmd:  "UID FETCH 501 (UID BODY.PEEK[1.1]<0.4096>)",
			resp: []string{"* 28 FETCH (UID 501 BODY[1.1]<0> " + lit(plain) + ")"},
		},
	)
	res, err := c.Search(SearchOptions{Snippets: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := uidsOf(res); !reflect.DeepEqual(got, []uint32{503, 502, 501}) {
		t.Fatalf("uids = %v", got)
	}
	want := []string{
		mailparse.DecodeSnippet([]byte(plain), "7bit", "text/plain", "UTF-8", 200),
		mailparse.DecodeSnippet([]byte(html), "quoted-printable", "text/html", "UTF-8", 200),
		mailparse.DecodeSnippet([]byte(plain), "7bit", "text/plain", "UTF-8", 200),
	}
	for i, m := range res.Messages {
		if m.Snippet != want[i] {
			t.Errorf("snippet %d = %q, want %q", i, m.Snippet, want[i])
		}
	}
}
