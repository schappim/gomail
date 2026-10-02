package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"gomail/internal/compose"
	"gomail/internal/model"
	"gomail/internal/smtpsend"
)

func (a *app) draftCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "draft",
		Aliases: []string{"drafts"},
		Short:   "Create, list, show, update, send and delete drafts",
		Long: `Drafts are stored in Gmail's Drafts folder, so they appear in the Gmail web
and mobile apps too. Reply drafts (gomail reply ID --draft) are threaded into
the conversation.

Updating a draft replaces it, so it gets a NEW id.`,
	}
	cmd.AddCommand(a.draftCreateCmd(), a.draftListCmd(), a.draftShowCmd(), a.draftUpdateCmd(), a.draftSendCmd(), a.draftDeleteCmd())
	return cmd
}

func (a *app) draftCreateCmd() *cobra.Command {
	var cf composeFlags
	cmd := &cobra.Command{
		Use:     "create",
		Aliases: []string{"new", "add"},
		Short:   "Save a new draft",
		Long:    `Compose a new message and save it to Drafts. Recipients are optional for drafts.` + composeHelp,
		Example: `  gomail draft create --to jane@example.com -s "Proposal" --markdown-file proposal.md --attach proposal.pdf
  gomail -p personal draft create -s "Notes" -b "todo" --no-signature`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runCompose(&cf, compose.Options{}, actionDraft)
		},
	}
	cf.registerMessage(cmd)
	return cmd
}

func (a *app) draftListCmd() *cobra.Command {
	var o listOpts
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List drafts",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runList(&o, "")
		},
	}
	o.register(cmd, "drafts")
	return cmd
}

func (a *app) draftShowCmd() *cobra.Command {
	cmd := a.readCmd()
	cmd.Use = "show ID"
	cmd.Aliases = []string{"read", "preview"}
	cmd.Short = "Show a draft (--open to view the HTML in a browser)"
	cmd.Example = "  gomail draft show 18c3f2a1b2c3d4e5 --open"
	return cmd
}

// fetchDraft loads a message and checks that it is a draft.
func (a *app) fetchDraft(id string) (*model.Message, error) {
	m, err := a.fetchMessage(id)
	if err != nil {
		return nil, err
	}
	for _, l := range m.Labels {
		if l == "DRAFT" {
			return m, nil
		}
	}
	for _, f := range m.Flags {
		if strings.EqualFold(f, `\Draft`) {
			return m, nil
		}
	}
	return nil, fmt.Errorf("message %s is not a draft", id)
}

func (a *app) draftUpdateCmd() *cobra.Command {
	var cf composeFlags
	var clearAttachments, keepRecipients bool
	cmd := &cobra.Command{
		Use:     "update ID",
		Aliases: []string{"edit"},
		Short:   "Change a draft (replaces it; prints the new draft id)",
		Long: `Change parts of an existing draft. Anything you don't pass is kept: giving
--to replaces the To list (likewise --cc/--bcc), giving a body replaces the
whole body (with the signature added again), and --attach adds files to the
existing attachments unless --clear-attachments is given.

Gmail drafts can't be edited in place over IMAP, so the old draft is deleted
and a new one saved, which gets a NEW id.`,
		Example: `  gomail draft update 18c3f2a1b2c3d4e5 -m "Updated **text**" -S no-phone
  gomail draft update 18c3f2a1b2c3d4e5 --to new@example.com --attach extra.pdf`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			old, err := a.fetchDraft(args[0])
			if err != nil {
				return err
			}
			base := compose.Options{
				To: old.To, Cc: old.Cc, Bcc: old.Bcc, ReplyTo: old.ReplyTo, Subject: old.Subject,
				InReplyTo: old.InReplyTo, References: old.References,
			}
			if len(cf.to) > 0 && !keepRecipients {
				base.To = nil
			}
			if len(cf.cc) > 0 && !keepRecipients {
				base.Cc = nil
			}
			if len(cf.bcc) > 0 && !keepRecipients {
				base.Bcc = nil
			}
			if !clearAttachments {
				for _, att := range old.Attachments {
					base.Attachments = append(base.Attachments, compose.File{
						Filename: att.Filename, ContentType: att.ContentType, Data: att.Data,
						ContentID: att.ContentID, Inline: att.Inline,
					})
				}
			}
			keepBody := !cf.hasBody()
			if keepBody {
				// The saved body already contains its signature and any quote.
				cf.noSignature = true
			}
			o, sig, err := a.applyFlags(&cf, base)
			if err != nil {
				return err
			}
			c, err := a.imap()
			if err != nil {
				return err
			}
			if keepBody {
				o.Text, o.HTML, sig = old.Text, old.HTML, ""
				if old.TextFromHTML {
					o.Text = "" // let compose derive it from the HTML again
				}
			} else if old.InReplyTo != "" {
				// A new body on a reply draft: quote the original again.
				o.Quote = a.findOriginal(old.InReplyTo)
			}
			if cf.from == "" && len(old.From) > 0 {
				o.From = old.From[0]
			}
			o.IncludeBcc = true
			b, err := compose.Build(o)
			if err != nil {
				return err
			}
			sum, err := c.AppendDraft(b.Raw)
			if err != nil {
				return err
			}
			if err := c.DeleteDraft(old.ID); err != nil {
				return fmt.Errorf("saved new draft %s but could not delete the old one %s: %w", sum.ID, old.ID, err)
			}
			if a.jsonOut {
				return a.printJSON(map[string]any{
					"draft": true, "id": sum.ID, "replaced": old.ID, "thread_id": sum.ThreadID,
					"subject": o.Subject, "signature": sig, "recipients": b.Recipients,
				})
			}
			a.printf("Updated draft: new id %s (replaced %s)\n", sum.ID, old.ID)
			return nil
		},
	}
	cf.registerMessage(cmd)
	f := cmd.Flags()
	f.BoolVar(&clearAttachments, "clear-attachments", false, "drop the draft's existing attachments")
	f.BoolVar(&keepRecipients, "add-recipients", false, "add --to/--cc/--bcc to the existing lists instead of replacing them")
	return cmd
}

func (a *app) draftSendCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "send ID",
		Short: "Send a saved draft (asks for confirmation unless --yes)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			draft, err := a.fetchDraft(args[0])
			if err != nil {
				return err
			}
			raw, _, rcpts, err := compose.PrepareForSend(draft.Raw)
			if err != nil {
				return err
			}
			cf := &composeFlags{yes: yes}
			show := func() error {
				a.printHeaderBlockTo(draft)
				return nil
			}
			if err := a.confirm(cf, fmt.Sprintf("Send draft to %s?", strings.Join(rcpts, ", ")), show); err != nil {
				return err
			}
			sc, err := a.smtpConfig()
			if err != nil {
				return err
			}
			p, _ := a.profile()
			if err := smtpsend.Send(sc, p.Email, rcpts, raw); err != nil {
				return err
			}
			c, err := a.imap()
			if err != nil {
				return err
			}
			delErr := c.DeleteDraft(draft.ID)
			if a.jsonOut {
				out := map[string]any{"sent": true, "draft_id": draft.ID, "message_id": draft.MessageID, "recipients": rcpts, "subject": draft.Subject}
				if delErr != nil {
					out["warning"] = "sent, but the draft could not be deleted: " + delErr.Error()
				}
				return a.printJSON(out)
			}
			a.printf("Sent draft %s to %s\n", draft.ID, strings.Join(rcpts, ", "))
			if delErr != nil {
				a.note("warning: sent, but the draft could not be deleted: %v", delErr)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "send without asking for confirmation")
	return cmd
}

// findOriginal fetches the message a reply draft answers, or nil if it can't
// be found (the reply is then saved without a quote).
func (a *app) findOriginal(messageID string) *model.Message {
	c, err := a.imap()
	if err != nil {
		return nil
	}
	sum, err := c.FindByMessageID(messageID)
	if err != nil {
		return nil
	}
	m, err := a.fetchMessage(sum.ID)
	if err != nil {
		return nil
	}
	return m
}

// printHeaderBlockTo shows a message summary and body on stderr (used before
// confirmation prompts).
func (a *app) printHeaderBlockTo(m *model.Message) {
	saved := a.out
	a.out = a.errOut
	a.printHeaderBlock(m)
	a.printf("\n%s\n%s\n", strings.TrimRight(m.Text, "\n"), strings.Repeat("─", 60))
	a.out = saved
}

func (a *app) draftDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete ID...",
		Aliases: []string{"rm", "discard"},
		Short:   "Discard drafts",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.imap()
			if err != nil {
				return err
			}
			for _, id := range args {
				if err := c.DeleteDraft(id); err != nil {
					return err
				}
			}
			if a.jsonOut {
				return a.printJSON(map[string]any{"deleted": args})
			}
			a.printf("Deleted %d draft%s\n", len(args), plural(len(args)))
			return nil
		},
	}
}
