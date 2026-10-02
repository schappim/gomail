package cli

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"

	"gomail/internal/model"
)

// termWidth is the stdout width, or 120 when not a terminal.
func termWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 40 {
		return w
	}
	return 120
}

func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// truncate shortens s to at most n display runes, adding an ellipsis.
func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

func pad(s string, n int) string {
	if c := utf8.RuneCountInString(s); c < n {
		return s + strings.Repeat(" ", n-c)
	}
	return s
}

func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

func shortDate(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func longDate(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("Mon, 2 Jan 2006 15:04 -0700")
}

// who returns a compact name for the first address.
func who(list []model.Address) string {
	if len(list) == 0 {
		return "-"
	}
	if list[0].Name != "" {
		return list[0].Name
	}
	return list[0].Email
}

// flagString renders U (unread), * (starred), @ (attachments).
func flagString(m *model.MessageSummary) string {
	var b strings.Builder
	if m.Unread {
		b.WriteByte('U')
	}
	if m.Starred {
		b.WriteByte('*')
	}
	if m.HasAttach {
		b.WriteByte('@')
	}
	return b.String()
}

// printMessageTable prints summaries as an aligned table.
func (a *app) printMessageTable(msgs []model.MessageSummary, snippets bool) {
	width := termWidth()
	const idW, dateW, fromW, flagW = 16, 16, 22, 3
	subjW := width - idW - dateW - fromW - flagW - 8
	if subjW < 20 {
		subjW = 20
	}
	a.printf("%s  %s  %s  %s  %s\n", pad("ID", idW), pad("DATE", dateW), pad("FROM/TO", fromW), pad("FL", flagW), "SUBJECT")
	for i := range msgs {
		m := &msgs[i]
		party := who(m.From)
		if len(m.From) > 0 && a.isSelf(m.From[0].Email) && len(m.To) > 0 {
			party = "to: " + who(m.To)
		}
		subject := m.Subject
		if subject == "" {
			subject = "(no subject)"
		}
		a.printf("%s  %s  %s  %s  %s\n", pad(m.ID, idW), pad(shortDate(m.Date), dateW), pad(truncate(party, fromW), fromW), pad(flagString(m), flagW), truncate(subject, subjW))
		if snippets && m.Snippet != "" {
			a.printf("%s  %s\n", strings.Repeat(" ", idW+dateW+fromW+flagW+6), truncate(m.Snippet, subjW))
		}
	}
}

// printHeaderBlock prints the common header lines of a message.
func (a *app) printHeaderBlock(m *model.Message) {
	a.printf("ID:       %s   Thread: %s\n", m.ID, m.ThreadID)
	a.printf("Date:     %s\n", longDate(m.Date))
	a.printf("From:     %s\n", model.FormatAddresses(m.From))
	if len(m.To) > 0 {
		a.printf("To:       %s\n", model.FormatAddresses(m.To))
	}
	if len(m.Cc) > 0 {
		a.printf("Cc:       %s\n", model.FormatAddresses(m.Cc))
	}
	if len(m.Bcc) > 0 {
		a.printf("Bcc:      %s\n", model.FormatAddresses(m.Bcc))
	}
	if len(m.ReplyTo) > 0 {
		a.printf("Reply-To: %s\n", model.FormatAddresses(m.ReplyTo))
	}
	a.printf("Subject:  %s\n", m.Subject)
	if len(m.Labels) > 0 {
		a.printf("Labels:   %s\n", strings.Join(m.Labels, ", "))
	}
	if len(m.Attachments) > 0 {
		a.printf("Attachments:\n")
		for _, att := range m.Attachments {
			inline := ""
			if att.Inline {
				inline = ", inline"
			}
			a.printf("  [%d] %s (%s, %s%s)\n", att.Index, att.Filename, att.ContentType, humanSize(int64(att.Size)), inline)
		}
	}
}

// stripQuoted removes quoted earlier messages from a plain-text body, for
// thread views where those messages are shown anyway. It cuts the first
// "> "-quoted block together with its attribution line ("On …, X wrote:"),
// keeping anything after the block (such as a signature placed below the
// quote), and cuts an Outlook-style "Original Message" or "From:/Sent:"
// header and everything below it. Interleaved replies, where a quote has no
// attribution and is followed by more writing, are left alone.
func stripQuoted(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	trimmed := func(i int) string { return strings.TrimSpace(lines[i]) }
	quoted := func(i int) bool { return strings.HasPrefix(trimmed(i), ">") }

	for i := 1; i < len(lines); i++ {
		if isOutlookHeader(lines, i) {
			lines = lines[:i]
			break
		}
	}

	if q := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), ">") }); q >= 0 {
		// The block runs over quoted and blank lines.
		last := q
		for j := q; j < len(lines) && (quoted(j) || trimmed(j) == ""); j++ {
			if quoted(j) {
				last = j
			}
		}
		// The attribution sits just above, possibly wrapped onto two lines.
		from := q
		for from > 0 && trimmed(from-1) == "" {
			from--
		}
		attributed := from > 0 && strings.HasSuffix(trimmed(from-1), ":")
		if attributed {
			from--
			if from > 0 && strings.HasPrefix(trimmed(from-1), "On ") && !strings.HasPrefix(trimmed(from), "On ") {
				from--
			}
		}
		restIsBlank := true
		for j := last + 1; j < len(lines); j++ {
			if trimmed(j) != "" {
				restIsBlank = false
			}
		}
		if attributed || restIsBlank {
			if !attributed {
				from = q
			}
			lines = append(lines[:from:from], lines[last+1:]...)
		}
	}

	out := strings.TrimSpace(strings.Join(lines, "\n"))
	out = multiBlank.ReplaceAllString(out, "\n\n")
	if out == "" {
		return text
	}
	return out
}

var multiBlank = regexp.MustCompile(`\n[ \t]*\n(?:[ \t]*\n)+`)

// isOutlookHeader reports whether line i starts an Outlook-style quoted
// message: an "-----Original Message-----" rule, or "From: …" directly
// followed within three lines by "Sent: …" or "Date: …" (but not the header
// of a forwarded message).
func isOutlookHeader(lines []string, i int) bool {
	l := strings.TrimSpace(lines[i])
	if strings.Contains(strings.ToLower(l), "original message") && strings.HasPrefix(l, "-") && strings.HasSuffix(l, "-") {
		return true
	}
	if !strings.HasPrefix(l, "From:") && !strings.HasPrefix(l, "*From:*") {
		return false
	}
	// A forwarded message's header looks the same but is content to keep.
	for j := i - 1; j >= 0 && j >= i-2; j-- {
		if strings.Contains(strings.ToLower(lines[j]), "forwarded message") {
			return false
		}
	}
	for j := i + 1; j < len(lines) && j <= i+3; j++ {
		n := strings.TrimLeft(strings.TrimSpace(lines[j]), "*")
		if strings.HasPrefix(n, "Sent:") || strings.HasPrefix(n, "Date:") {
			return true
		}
	}
	return false
}

// inlineCIDs rewrites cid: references in HTML to data: URIs so a saved HTML
// file shows its embedded images when opened in a browser.
func inlineCIDs(html string, atts []model.Attachment) string {
	for _, att := range atts {
		if att.ContentID == "" || len(att.Data) == 0 {
			continue
		}
		uri := "data:" + att.ContentType + ";base64," + base64.StdEncoding.EncodeToString(att.Data)
		html = strings.ReplaceAll(html, "cid:"+att.ContentID, uri)
	}
	return html
}

// openInBrowser opens a local file with the platform's default handler.
func openInBrowser(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}

// writeTempHTML saves HTML to a temp file and returns its path.
func writeTempHTML(prefix, html string) (string, error) {
	f, err := os.CreateTemp("", prefix+"-*.html")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(html); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// safeFilename turns an arbitrary attachment or subject string into a safe
// file name component.
func safeFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 32 || r == 127:
			continue
		case strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.TrimSpace(b.String())
	s = strings.Trim(s, ".")
	if s == "" {
		s = "unnamed"
	}
	if len(s) > 200 {
		ext := filepath.Ext(s)
		if len(ext) > 20 {
			ext = ""
		}
		s = truncateBytes(s, 200-len(ext)) + ext
	}
	return s
}

func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// slug makes a short lowercase directory-friendly version of a subject.
func slug(s string, max int) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if utf8.RuneCountInString(out) > max {
		out = strings.Trim(string([]rune(out)[:max]), "-")
	}
	if out == "" {
		out = "no-subject"
	}
	return out
}

// uniquePath returns path, or "name (2).ext", "name (3).ext"... if taken.
func uniquePath(dir, name string, taken map[string]bool) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	candidate := name
	for i := 2; ; i++ {
		p := filepath.Join(dir, candidate)
		if !taken[strings.ToLower(p)] {
			if _, err := os.Stat(p); os.IsNotExist(err) {
				taken[strings.ToLower(p)] = true
				return p
			}
		}
		candidate = fmt.Sprintf("%s (%d)%s", base, i, ext)
	}
}
