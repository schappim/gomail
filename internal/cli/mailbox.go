package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"gomail/internal/gmail"
	"gomail/internal/model"
)

func (a *app) labelsCmd() *cobra.Command {
	var counts bool
	cmd := &cobra.Command{
		Use:     "labels",
		Aliases: []string{"folders", "mailboxes"},
		Short:   "List labels/folders (Inbox, Sent, Drafts, user labels...)",
		Long: `List every Gmail label as exposed over IMAP. System labels show their role
(inbox, sent, drafts, all, trash, spam, starred, important); those role names
work anywhere a label is accepted, e.g. "gomail list sent".`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.imap()
			if err != nil {
				return err
			}
			labels, err := c.Labels(counts)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(map[string]any{"labels": labels})
			}
			for _, l := range labels {
				role := ""
				if l.Role != "" {
					role = "(" + l.Role + ")"
				}
				line := fmt.Sprintf("%-40s %-12s", l.Name, role)
				if l.Messages != nil {
					line += fmt.Sprintf(" %7d messages", *l.Messages)
				}
				if l.Unread != nil && *l.Unread > 0 {
					line += fmt.Sprintf(" %6d unread", *l.Unread)
				}
				if !l.Selectable {
					line += "  [not selectable]"
				}
				a.printf("%s\n", strings.TrimRight(line, " "))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&counts, "counts", false, "include total and unread counts (one STATUS per label)")
	return cmd
}

func (a *app) labelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "label",
		Short: "Create labels",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "create NAME",
		Short: "Create a label (use / for nesting, e.g. Clients/Acme)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.imap()
			if err != nil {
				return err
			}
			if err := c.CreateLabel(args[0]); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(map[string]string{"created": args[0]})
			}
			a.printf("Created label %q\n", args[0])
			return nil
		},
	})
	return cmd
}

type listOpts struct {
	label    string
	limit    int
	offset   int
	page     int
	snippets bool
	unread   bool
}

func (o *listOpts) register(cmd *cobra.Command, defaultLabel string) {
	f := cmd.Flags()
	f.StringVarP(&o.label, "label", "l", defaultLabel, "label/folder to look in: inbox, sent, drafts, all, trash, spam, starred, important, or a label name")
	f.IntVarP(&o.limit, "limit", "n", 25, "maximum number of messages")
	f.IntVar(&o.offset, "offset", 0, "skip this many of the newest matches")
	f.IntVar(&o.page, "page", 0, "page number (1-based), an alternative to --offset")
	f.BoolVar(&o.snippets, "snippets", false, "include a short body preview for each message")
	f.BoolVar(&o.unread, "unread", false, "only unread messages")
}

func (a *app) runList(o *listOpts, query string) error {
	if o.limit <= 0 {
		return usageErrorf("--limit must be positive")
	}
	if o.page > 0 {
		o.offset = (o.page - 1) * o.limit
	}
	if o.unread {
		query = strings.TrimSpace(query + " is:unread")
	}
	c, err := a.imap()
	if err != nil {
		return err
	}
	res, err := c.Search(gmail.SearchOptions{Mailbox: o.label, Query: query, Limit: o.limit, Offset: o.offset, Snippets: o.snippets})
	if err != nil {
		return err
	}
	if a.jsonOut {
		msgs := res.Messages
		if msgs == nil {
			msgs = []model.MessageSummary{}
		}
		return a.printJSON(map[string]any{
			"mailbox": res.Mailbox, "query": query, "total": res.Total,
			"offset": o.offset, "count": len(msgs), "messages": msgs,
		})
	}
	desc := res.Mailbox
	if query != "" {
		desc += fmt.Sprintf(" matching %q", query)
	}
	if len(res.Messages) == 0 {
		a.printf("No messages in %s.\n", desc)
		return nil
	}
	a.printf("%d–%d of %d in %s\n\n", o.offset+1, o.offset+len(res.Messages), res.Total, desc)
	a.printMessageTable(res.Messages, o.snippets)
	if more := res.Total - o.offset - len(res.Messages); more > 0 {
		a.note("\n%d more; use --offset %d to see the next page", more, o.offset+len(res.Messages))
	}
	return nil
}

func (a *app) searchCmd() *cobra.Command {
	var o listOpts
	cmd := &cobra.Command{
		Use:     "search QUERY...",
		Aliases: []string{"find", "s"},
		Short:   "Search with Gmail search syntax (defaults to All Mail)",
		Long: `Search messages using Gmail's own search syntax, exactly as in the Gmail
search box. Searches All Mail unless --label is given.

Useful operators: from: to: cc: subject: "exact phrase" has:attachment
filename:pdf in:inbox in:sent in:drafts label:NAME is:unread is:starred
after:2026/01/01 before:2026/02/01 older_than:7d newer_than:2d larger:5M
rfc822msgid:<id> -word {a b} (OR)`,
		Example: `  gomail search invoice from:acme.com has:attachment
  gomail search 'subject:"purchase order" after:2026/09/01' --limit 50 --json
  gomail -p work search is:unread --label inbox --snippets`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runList(&o, strings.Join(args, " "))
		},
	}
	o.register(cmd, "all")
	return cmd
}

func (a *app) listCmd() *cobra.Command {
	var o listOpts
	cmd := &cobra.Command{
		Use:     "list [LABEL]",
		Aliases: []string{"ls"},
		Short:   "List the newest messages in a label/folder (default inbox)",
		Example: `  gomail list
  gomail list sent -n 10
  gomail list "Clients/Acme" --unread --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				if cmd.Flags().Changed("label") {
					return usageErrorf("give the label either as an argument or with --label")
				}
				o.label = args[0]
			}
			return a.runList(&o, "")
		},
	}
	o.register(cmd, "inbox")
	return cmd
}
