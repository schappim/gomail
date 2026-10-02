package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"gomail/internal/gmail"
)

func (a *app) modifyCmd() *cobra.Command {
	var add, remove []string
	var read, unread, star, unstar, archive, thread bool
	cmd := &cobra.Command{
		Use:     "modify ID...",
		Aliases: []string{"mark", "label-message"},
		Short:   "Add/remove labels, mark read/unread, star, archive",
		Example: `  gomail modify 18c3f2a1b2c3d4e5 --read --add-label "Clients/Acme" --archive
  gomail modify 18c3f2a1b2c3d4e5 --thread --unread --star
  gomail modify 18c3f2a1b2c3d4e5 --remove-label important`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if read && unread {
				return usageErrorf("--read and --unread conflict")
			}
			if star && unstar {
				return usageErrorf("--star and --unstar conflict")
			}
			if !(read || unread || star || unstar || archive) && len(add)+len(remove) == 0 {
				return usageErrorf("nothing to do: give --add-label, --remove-label, --read, --unread, --star, --unstar or --archive")
			}
			c, err := a.imap()
			if err != nil {
				return err
			}
			ids, err := a.resolveIDs(c, args, thread)
			if err != nil {
				return err
			}
			var done []string
			if len(add)+len(remove) > 0 {
				if add, err = canonicalLabels(c, add); err != nil {
					return err
				}
				if remove, err = canonicalLabels(c, remove); err != nil {
					return err
				}
				if err := c.ModifyLabels(ids, add, remove); err != nil {
					return err
				}
				for _, l := range add {
					done = append(done, "+"+l)
				}
				for _, l := range remove {
					done = append(done, "-"+l)
				}
			}
			if read || unread {
				if err := c.MarkRead(ids, read); err != nil {
					return err
				}
				done = append(done, map[bool]string{true: "read", false: "unread"}[read])
			}
			if star || unstar {
				if err := c.Star(ids, star); err != nil {
					return err
				}
				done = append(done, map[bool]string{true: "starred", false: "unstarred"}[star])
			}
			if archive {
				if err := c.Archive(ids); err != nil {
					return err
				}
				done = append(done, "archived")
			}
			if a.jsonOut {
				return a.printJSON(map[string]any{"ids": ids, "changes": done})
			}
			a.printf("%d message%s: %s\n", len(ids), plural(len(ids)), strings.Join(done, ", "))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&add, "add-label", nil, "add a label (repeatable; inbox, starred and important work too)")
	f.StringArrayVar(&remove, "remove-label", nil, "remove a label (repeatable)")
	f.BoolVar(&read, "read", false, "mark as read")
	f.BoolVar(&unread, "unread", false, "mark as unread")
	f.BoolVar(&star, "star", false, "star")
	f.BoolVar(&unstar, "unstar", false, "remove the star")
	f.BoolVar(&archive, "archive", false, "remove from the inbox")
	f.BoolVar(&thread, "thread", false, "apply to every message in each id's thread")
	return cmd
}

func (a *app) archiveCmd() *cobra.Command {
	var thread bool
	cmd := &cobra.Command{
		Use:   "archive ID...",
		Short: "Remove messages from the inbox (they stay in All Mail)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.imap()
			if err != nil {
				return err
			}
			ids, err := a.resolveIDs(c, args, thread)
			if err != nil {
				return err
			}
			if err := c.Archive(ids); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(map[string]any{"archived": ids})
			}
			a.printf("Archived %d message%s\n", len(ids), plural(len(ids)))
			return nil
		},
	}
	cmd.Flags().BoolVar(&thread, "thread", false, "archive every message in each id's thread")
	return cmd
}

func (a *app) trashCmd() *cobra.Command {
	var thread bool
	cmd := &cobra.Command{
		Use:     "trash ID...",
		Aliases: []string{"delete", "rm"},
		Short:   "Move messages to Trash (Gmail empties it after 30 days)",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.imap()
			if err != nil {
				return err
			}
			ids, err := a.resolveIDs(c, args, thread)
			if err != nil {
				return err
			}
			if err := c.Trash(ids); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(map[string]any{"trashed": ids})
			}
			a.printf("Moved %d message%s to Trash\n", len(ids), plural(len(ids)))
			return nil
		},
	}
	cmd.Flags().BoolVar(&thread, "thread", false, "trash every message in each id's thread")
	return cmd
}

// canonicalLabels checks that user labels exist and returns their exact
// names; otherwise Gmail would silently create a new label for a typo. The
// system labels inbox, starred and important pass through.
func canonicalLabels(c *gmail.Client, names []string) ([]string, error) {
	out := make([]string, 0, len(names))
	for _, n := range names {
		switch strings.ToLower(n) {
		case "inbox", "starred", "important":
			out = append(out, n)
			continue
		}
		mb, err := c.ResolveMailbox(n)
		if err != nil {
			return nil, fmt.Errorf("label %q doesn't exist (create it with `gomail label create %q`): %w", n, n, err)
		}
		out = append(out, mb)
	}
	return out, nil
}
