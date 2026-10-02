package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStripQuoted(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"no quote", "Hello\nthere", "Hello\nthere"},
		{"gmail reply", "Thanks!\n\nOn Thu, 2 Oct 2026 at 11:15, Jane <jane@x.com> wrote:\n> hi\n>\n> there\n", "Thanks!"},
		{"wrapped attribution", "Sure.\n\nOn Thu, 2 Oct 2026 at 11:15, Jane Smith <\njane@x.com> wrote:\n\n> hi\n", "Sure."},
		{"signature below quote kept", "Can you check the address?\n\nSam\n\nOn Thu, 1 Oct 2026 at 14:23, Courier Support <support@courier.example>\nwrote:\n\n>\n> Hi Sam,\n>> nested\n>\n\n\n-- \nSam Example", "Can you check the address?\n\nSam\n\n-- \nSam Example"},
		{"interleaved reply kept", "> question?\nanswer\n", "> question?\nanswer"},
		{"trailing quote without attribution", "Agreed.\n\n> earlier text\n> more\n", "Agreed."},
		{"all quoted", "> only quote\n", "> only quote\n"},
		{"signature kept", "Body\n\nAlex\n+61 400 000 000\n\nOn Mon, Jane wrote:\n> x", "Body\n\nAlex\n+61 400 000 000"},
		{"outlook header", "Fine by me.\n\nFrom: Jane Smith <jane@x.com>\nSent: Thursday, 1 October 2026 2:04 PM\nTo: Bob\nSubject: Re: x\n\nold text", "Fine by me."},
		{"original message rule", "Yes.\n\n--------------- Original Message ---------------\n*From:* Sam\nold", "Yes."},
		{"forward kept", "FYI\n\n---------- Forwarded message ---------\nFrom: Jane <jane@x.com>\nDate: Thu, 1 Oct 2026\nSubject: x\n\nbody", "FYI\n\n---------- Forwarded message ---------\nFrom: Jane <jane@x.com>\nDate: Thu, 1 Oct 2026\nSubject: x\n\nbody"},
	}
	for _, c := range cases {
		if got := stripQuoted(c.in); got != c.want {
			t.Errorf("%s: stripQuoted = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestSafeFilename(t *testing.T) {
	cases := map[string]string{
		"invoice.pdf":           "invoice.pdf",
		"../../etc/passwd":      "passwd",
		`C:\Users\x\report.doc`: "report.doc",
		"a:b*c?.txt":            "a_b_c_.txt",
		"":                      "unnamed",
		"..":                    "unnamed",
		"Rechnung für März.pdf": "Rechnung für März.pdf",
	}
	for in, want := range cases {
		if got := safeFilename(in); got != want {
			t.Errorf("safeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlugAndFence(t *testing.T) {
	if got := slug("Re: Quote for 50× Arduino boards!!", 30); got != "re-quote-for-50-arduino-boards" {
		t.Errorf("slug = %q", got)
	}
	if got := slug("!!!", 10); got != "no-subject" {
		t.Errorf("slug empty = %q", got)
	}
	if got := codeFence("plain"); got != "```" {
		t.Errorf("fence = %q", got)
	}
	if got := codeFence("has ```` four"); got != "`````" {
		t.Errorf("fence = %q", got)
	}
}

func TestUniquePath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.pdf"), nil, 0o644)
	taken := map[string]bool{}
	if got := uniquePath(dir, "a.pdf", taken); filepath.Base(got) != "a (2).pdf" {
		t.Errorf("first = %q", got)
	}
	if got := uniquePath(dir, "a.pdf", taken); filepath.Base(got) != "a (3).pdf" {
		t.Errorf("second = %q", got)
	}
}
