package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"gomail/internal/gmail"
	"gomail/internal/mailparse"
	"gomail/internal/model"
)

// fetchMessage downloads and parses one message by Gmail id.
func (a *app) fetchMessage(id string) (*model.Message, error) {
	c, err := a.imap()
	if err != nil {
		return nil, err
	}
	raw, sum, err := c.FetchRaw(id)
	if err != nil {
		return nil, err
	}
	return parseWithSummary(raw, sum)
}

// parseWithSummary parses raw and copies the IMAP-side fields onto it.
func parseWithSummary(raw []byte, sum *model.MessageSummary) (*model.Message, error) {
	msg, err := mailparse.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parsing message %s: %w", sum.ID, err)
	}
	msg.ID = sum.ID
	msg.ThreadID = sum.ThreadID
	msg.Labels = sum.Labels
	msg.Flags = sum.Flags
	msg.Unread = sum.Unread
	msg.Starred = sum.Starred
	msg.Mailbox = sum.Mailbox
	msg.UID = sum.UID
	if msg.Date.IsZero() {
		msg.Date = sum.Date
	}
	if msg.Subject == "" {
		msg.Subject = sum.Subject
	}
	if len(msg.From) == 0 {
		msg.From = sum.From
	}
	return msg, nil
}

// messageJSON is the JSON shape of a full message. HTML and the full header
// list are opt-in because they are large.
type messageJSON struct {
	*model.Message
	HasHTML bool `json:"has_html"`
}

func toMessageJSON(m *model.Message, withHTML, withHeaders bool) messageJSON {
	cp := *m
	out := messageJSON{Message: &cp, HasHTML: m.HTML != ""}
	if !withHTML {
		cp.HTML = ""
	}
	if !withHeaders {
		cp.Headers = nil
	}
	if cp.Attachments == nil {
		cp.Attachments = []model.Attachment{}
	}
	return out
}

func (a *app) readCmd() *cobra.Command {
	var html, raw, headers, open, markRead bool
	cmd := &cobra.Command{
		Use:     "read ID",
		Aliases: []string{"show", "get", "cat"},
		Short:   "Show a message (text by default; --html, --raw, --open)",
		Long: `Show one message. Prints the plain-text body by default. If the message is
HTML-only, the text is derived from the HTML. Reading does not mark the
message as read unless --mark-read is given.`,
		Example: `  gomail read 18c3f2a1b2c3d4e5
  gomail read 18c3f2a1b2c3d4e5 --open        # view the HTML version in a browser
  gomail read 18c3f2a1b2c3d4e5 --json --html # include the HTML body in JSON`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			msg, err := a.fetchMessage(args[0])
			if err != nil {
				return err
			}
			if markRead && msg.Unread {
				c, _ := a.imap()
				if err := c.MarkRead([]string{msg.ID}, true); err != nil {
					return err
				}
				msg.Unread = false
			}
			if raw {
				_, err := a.out.Write(msg.Raw)
				return err
			}
			if open {
				if err := a.openMessageHTML(msg); err != nil {
					return err
				}
			}
			if a.jsonOut {
				return a.printJSON(toMessageJSON(msg, html, headers))
			}
			a.printHeaderBlock(msg)
			if headers {
				a.printf("\n--- headers ---\n")
				for _, h := range msg.Headers {
					a.printf("%s: %s\n", h.Name, h.Value)
				}
			}
			a.printf("\n")
			switch {
			case html && msg.HTML != "":
				a.printf("%s\n", msg.HTML)
			case html:
				a.note("(message has no HTML part; showing text)")
				a.printf("%s\n", msg.Text)
			default:
				a.printf("%s\n", strings.TrimRight(msg.Text, "\n"))
				if msg.TextFromHTML {
					a.note("\n(text derived from the HTML part; use --open or --html to see the original)")
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&html, "html", false, "print the HTML body (and include it in --json)")
	f.BoolVar(&raw, "raw", false, "print the raw RFC 822 message")
	f.BoolVar(&headers, "headers", false, "show all headers")
	f.BoolVar(&open, "open", false, "open the HTML body in a browser (embedded images included)")
	f.BoolVar(&markRead, "mark-read", false, "mark the message as read")
	return cmd
}

// openMessageHTML writes the message's HTML (or its text, escaped) to a temp
// file and opens it in the browser.
func (a *app) openMessageHTML(m *model.Message) error {
	body := m.HTML
	if body == "" {
		body = "<pre style=\"white-space:pre-wrap;font-family:sans-serif\">" + htmlEscape(m.Text) + "</pre>"
	}
	page := "<!DOCTYPE html><meta charset=\"utf-8\"><title>" + htmlEscape(m.Subject) + "</title>" + inlineCIDs(body, m.Attachments)
	path, err := writeTempHTML("gomail-"+m.ID, page)
	if err != nil {
		return err
	}
	a.note("Opened %s", path)
	return openInBrowser(path)
}

var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

func htmlEscape(s string) string { return htmlEscaper.Replace(s) }

// fetchThread downloads and parses every message in a thread, oldest first.
func (a *app) fetchThread(id string) ([]*model.Message, error) {
	c, err := a.imap()
	if err != nil {
		return nil, err
	}
	raws, err := c.Thread(id, true)
	if err != nil {
		return nil, err
	}
	msgs := make([]*model.Message, 0, len(raws))
	for i := range raws {
		m, err := parseWithSummary(raws[i].Raw, &raws[i].Summary)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
}

// participants lists the distinct senders and recipients in a thread.
func participants(msgs []*model.Message) []model.Address {
	seen := map[string]bool{}
	var out []model.Address
	add := func(list []model.Address) {
		for _, ad := range list {
			k := strings.ToLower(ad.Email)
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, ad)
		}
	}
	for _, m := range msgs {
		add(m.From)
	}
	for _, m := range msgs {
		add(m.To)
		add(m.Cc)
	}
	return out
}

type threadJSON struct {
	ThreadID     string          `json:"thread_id"`
	Subject      string          `json:"subject"`
	Count        int             `json:"count"`
	Participants []model.Address `json:"participants"`
	Messages     []messageJSON   `json:"messages"`
}

func (a *app) threadCmd() *cobra.Command {
	var full, html bool
	cmd := &cobra.Command{
		Use:     "thread ID",
		Aliases: []string{"conversation", "t"},
		Short:   "Show a whole conversation (accepts a thread id or any message id in it)",
		Long: `Show every message in a conversation, oldest first. Quoted reply text is
stripped from each message by default because the earlier messages are shown
anyway; use --full to keep it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			msgs, err := a.fetchThread(args[0])
			if err != nil {
				return err
			}
			if !full {
				for _, m := range msgs {
					m.Text = stripQuoted(m.Text)
				}
			}
			subject := msgs[0].Subject
			if a.jsonOut {
				out := threadJSON{ThreadID: msgs[0].ThreadID, Subject: subject, Count: len(msgs), Participants: participants(msgs)}
				for _, m := range msgs {
					out.Messages = append(out.Messages, toMessageJSON(m, html, false))
				}
				return a.printJSON(out)
			}
			a.printf("Thread %s: %s (%d message%s)\n", msgs[0].ThreadID, subject, len(msgs), plural(len(msgs)))
			var names []string
			for _, p := range participants(msgs) {
				names = append(names, p.String())
			}
			a.printf("Participants: %s\n", strings.Join(names, ", "))
			for i, m := range msgs {
				a.printf("\n%s\n", strings.Repeat("━", min(termWidth(), 100)))
				a.printf("[%d/%d] %s — %s\n", i+1, len(msgs), model.FormatAddresses(m.From), longDate(m.Date))
				a.printf("ID: %s", m.ID)
				if len(m.To) > 0 {
					a.printf("   To: %s", model.FormatAddresses(m.To))
				}
				if len(m.Cc) > 0 {
					a.printf("   Cc: %s", model.FormatAddresses(m.Cc))
				}
				a.printf("\n")
				if m.Subject != subject {
					a.printf("Subject: %s\n", m.Subject)
				}
				for _, att := range m.Attachments {
					if !att.Inline {
						a.printf("Attachment [%d]: %s (%s)\n", att.Index, att.Filename, humanSize(int64(att.Size)))
					}
				}
				a.printf("\n%s\n", strings.TrimRight(m.Text, "\n"))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "keep quoted reply text")
	cmd.Flags().BoolVar(&html, "html", false, "include HTML bodies in --json output")
	return cmd
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// resolveIDs expands message ids to every message in their threads when
// wholeThread is set.
func (a *app) resolveIDs(c *gmail.Client, ids []string, wholeThread bool) ([]string, error) {
	if !wholeThread {
		return ids, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		msgs, err := c.Thread(id, false)
		if err != nil {
			return nil, err
		}
		for _, m := range msgs {
			if !seen[m.Summary.ID] {
				seen[m.Summary.ID] = true
				out = append(out, m.Summary.ID)
			}
		}
	}
	return out, nil
}
