package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"gomail/internal/model"
)

func (a *app) attachmentsCmd() *cobra.Command {
	var outDir, nameGlob string
	var indexes []int
	var includeInline, toStdout bool
	cmd := &cobra.Command{
		Use:     "attachments ID",
		Aliases: []string{"attachment", "att"},
		Short:   "List or download a message's attachments",
		Long: `Without -o or --stdout, list the attachments of a message. With -o DIR, save
them into DIR (created if needed). Inline images such as signature logos are
skipped unless --include-inline is given or they are picked with --index.
Existing files are never overwritten; a numbered name is used instead.`,
		Example: `  gomail attachments 18c3f2a1b2c3d4e5
  gomail attachments 18c3f2a1b2c3d4e5 -o ~/Downloads
  gomail attachments 18c3f2a1b2c3d4e5 --index 2 --stdout > invoice.pdf
  gomail attachments 18c3f2a1b2c3d4e5 -o . --name '*.pdf'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			msg, err := a.fetchMessage(args[0])
			if err != nil {
				return err
			}
			selected, err := selectAttachments(msg.Attachments, indexes, nameGlob, includeInline)
			if err != nil {
				return err
			}
			switch {
			case toStdout:
				if len(selected) != 1 {
					return usageErrorf("--stdout needs exactly one attachment (matched %d); narrow it with --index or --name", len(selected))
				}
				_, err := a.out.Write(selected[0].Data)
				return err
			case outDir != "":
				saved, err := saveAttachments(outDir, selected, "")
				if err != nil {
					return err
				}
				if a.jsonOut {
					return a.printJSON(map[string]any{"message_id": msg.ID, "saved": saved})
				}
				if len(saved) == 0 {
					a.printf("No attachments to save.\n")
				}
				for _, s := range saved {
					a.printf("%s (%s)\n", s.Path, humanSize(int64(s.Size)))
				}
				return nil
			}
			if a.jsonOut {
				list := msg.Attachments
				if list == nil {
					list = []model.Attachment{}
				}
				return a.printJSON(map[string]any{"message_id": msg.ID, "subject": msg.Subject, "attachments": list})
			}
			if len(msg.Attachments) == 0 {
				a.printf("Message %s has no attachments.\n", msg.ID)
				return nil
			}
			for _, att := range msg.Attachments {
				kind := ""
				if att.Inline {
					kind = "  inline"
				}
				a.printf("[%d] %-40s %-28s %10s%s\n", att.Index, att.Filename, att.ContentType, humanSize(int64(att.Size)), kind)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&outDir, "output", "o", "", "save attachments into this directory")
	f.IntSliceVar(&indexes, "index", nil, "only these attachment numbers (repeatable or comma-separated)")
	f.StringVar(&nameGlob, "name", "", "only attachments whose filename matches this glob, e.g. '*.pdf'")
	f.BoolVar(&includeInline, "include-inline", false, "also save inline images")
	f.BoolVar(&toStdout, "stdout", false, "write a single attachment's bytes to stdout")
	return cmd
}

func selectAttachments(all []model.Attachment, indexes []int, glob string, includeInline bool) ([]model.Attachment, error) {
	if len(indexes) > 0 {
		var out []model.Attachment
		for _, idx := range indexes {
			found := false
			for _, att := range all {
				if att.Index == idx {
					out = append(out, att)
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("no attachment number %d (message has %d)", idx, len(all))
			}
		}
		return out, nil
	}
	var out []model.Attachment
	for _, att := range all {
		if att.Inline && !includeInline {
			continue
		}
		if glob != "" {
			ok, err := filepath.Match(strings.ToLower(glob), strings.ToLower(att.Filename))
			if err != nil {
				return nil, usageErrorf("bad --name pattern: %v", err)
			}
			if !ok {
				continue
			}
		}
		out = append(out, att)
	}
	return out, nil
}

type savedFile struct {
	Index       int    `json:"index,omitempty"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
	Size        int    `json:"size"`
	Path        string `json:"path"`
}

// saveAttachments writes attachments into dir, never overwriting.
func saveAttachments(dir string, atts []model.Attachment, prefix string) ([]savedFile, error) {
	saved := []savedFile{}
	if len(atts) == 0 {
		return saved, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	taken := map[string]bool{}
	for _, att := range atts {
		path := uniquePath(dir, prefix+safeFilename(att.Filename), taken)
		if err := os.WriteFile(path, att.Data, 0o644); err != nil {
			return saved, err
		}
		saved = append(saved, savedFile{Index: att.Index, Filename: att.Filename, ContentType: att.ContentType, Size: len(att.Data), Path: path})
	}
	return saved, nil
}

type downloadedMessage struct {
	Index       int             `json:"index"`
	ID          string          `json:"id"`
	MessageID   string          `json:"message_id,omitempty"`
	Date        string          `json:"date"`
	From        []model.Address `json:"from"`
	To          []model.Address `json:"to,omitempty"`
	Cc          []model.Address `json:"cc,omitempty"`
	Subject     string          `json:"subject"`
	Labels      []string        `json:"labels"`
	Text        string          `json:"text"`
	EML         string          `json:"eml"`
	HTML        string          `json:"html_file,omitempty"`
	Attachments []savedFile     `json:"attachments"`
}

type downloadManifest struct {
	ThreadID     string              `json:"thread_id"`
	Subject      string              `json:"subject"`
	Count        int                 `json:"count"`
	Participants []model.Address     `json:"participants"`
	Dir          string              `json:"dir"`
	Transcript   string              `json:"transcript"`
	Messages     []downloadedMessage `json:"messages"`
}

func (a *app) downloadCmd() *cobra.Command {
	var outDir string
	var onlyMessage, noAttachments, includeInline, full bool
	cmd := &cobra.Command{
		Use:     "download ID",
		Aliases: []string{"export", "dl"},
		Short:   "Download a whole thread (or one message) with attachments to a folder",
		Long: `Download the conversation containing ID (a thread id or any message id in it)
into a folder:

  thread.md           readable transcript (quoted text stripped unless --full)
  thread.json         metadata, text bodies and a manifest of saved files
  messages/NN-ID.eml  each original message, byte for byte
  messages/NN-ID.html the HTML body with embedded images inlined
  attachments/        every attachment (inline images only with --include-inline)

The folder defaults to ./<thread-id>-<subject>; choose it with -o.`,
		Example: `  gomail download 18c3f2a1b2c3d4e5
  gomail download 18c3f2a1b2c3d4e5 -o ~/cases/acme --json
  gomail download 18c3f2a1b2c3d4e5 --message   # just that message`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var msgs []*model.Message
			if onlyMessage {
				m, err := a.fetchMessage(args[0])
				if err != nil {
					return err
				}
				msgs = []*model.Message{m}
			} else {
				var err error
				if msgs, err = a.fetchThread(args[0]); err != nil {
					return err
				}
			}
			first := msgs[0]
			if outDir == "" {
				id := first.ThreadID
				if onlyMessage {
					id = first.ID
				}
				outDir = id + "-" + slug(first.Subject, 50)
			}
			manifest, err := writeDownload(outDir, msgs, !noAttachments, includeInline, full)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(manifest)
			}
			nAtt := 0
			for _, m := range manifest.Messages {
				nAtt += len(m.Attachments)
			}
			a.printf("Saved %d message%s and %d attachment%s to %s\n", len(msgs), plural(len(msgs)), nAtt, plural(nAtt), manifest.Dir)
			a.printf("Transcript: %s\n", manifest.Transcript)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&outDir, "output", "o", "", "destination folder")
	f.BoolVar(&onlyMessage, "message", false, "download only this message, not its whole thread")
	f.BoolVar(&noAttachments, "no-attachments", false, "skip attachments")
	f.BoolVar(&includeInline, "include-inline", false, "also save inline images as files")
	f.BoolVar(&full, "full", false, "keep quoted reply text in thread.md")
	return cmd
}

func writeDownload(dir string, msgs []*model.Message, withAttachments, includeInline, full bool) (*downloadManifest, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	msgDir := filepath.Join(abs, "messages")
	if err := os.MkdirAll(msgDir, 0o755); err != nil {
		return nil, err
	}
	first := msgs[0]
	man := &downloadManifest{
		ThreadID: first.ThreadID, Subject: first.Subject, Count: len(msgs),
		Participants: participants(msgs), Dir: abs,
		Transcript: filepath.Join(abs, "thread.md"),
	}
	var md strings.Builder
	fmt.Fprintf(&md, "# %s\n\n", mdEscapeLine(first.Subject))
	fmt.Fprintf(&md, "- Thread: `%s`\n- Messages: %d\n- Participants: %s\n", first.ThreadID, len(msgs), mdEscapeLine(model.FormatAddresses(man.Participants)))

	for i, m := range msgs {
		n := i + 1
		base := fmt.Sprintf("%02d-%s", n, m.ID)
		dm := downloadedMessage{
			Index: n, ID: m.ID, MessageID: m.MessageID, Date: m.Date.Format("2006-01-02T15:04:05Z07:00"),
			From: m.From, To: m.To, Cc: m.Cc, Subject: m.Subject, Labels: m.Labels,
			Text: m.Text, Attachments: []savedFile{},
		}
		dm.EML = filepath.Join(msgDir, base+".eml")
		if err := os.WriteFile(dm.EML, m.Raw, 0o644); err != nil {
			return nil, err
		}
		if m.HTML != "" {
			dm.HTML = filepath.Join(msgDir, base+".html")
			page := "<!DOCTYPE html><meta charset=\"utf-8\"><title>" + htmlEscape(m.Subject) + "</title>" + inlineCIDs(m.HTML, m.Attachments)
			if err := os.WriteFile(dm.HTML, []byte(page), 0o644); err != nil {
				return nil, err
			}
		}
		if withAttachments {
			var atts []model.Attachment
			for _, att := range m.Attachments {
				if !att.Inline || includeInline {
					atts = append(atts, att)
				}
			}
			prefix := ""
			if len(msgs) > 1 {
				prefix = fmt.Sprintf("%02d-", n)
			}
			saved, err := saveAttachments(filepath.Join(abs, "attachments"), atts, prefix)
			if err != nil {
				return nil, err
			}
			dm.Attachments = saved
		}
		man.Messages = append(man.Messages, dm)

		text := m.Text
		if !full {
			text = stripQuoted(text)
		}
		fmt.Fprintf(&md, "\n---\n\n## %d. %s — %s\n\n", n, mdEscapeLine(model.FormatAddresses(m.From)), m.Date.Local().Format("Mon, 2 Jan 2006 15:04 -0700"))
		fmt.Fprintf(&md, "- ID: `%s`\n", m.ID)
		if len(m.To) > 0 {
			fmt.Fprintf(&md, "- To: %s\n", mdEscapeLine(model.FormatAddresses(m.To)))
		}
		if len(m.Cc) > 0 {
			fmt.Fprintf(&md, "- Cc: %s\n", mdEscapeLine(model.FormatAddresses(m.Cc)))
		}
		if m.Subject != first.Subject {
			fmt.Fprintf(&md, "- Subject: %s\n", mdEscapeLine(m.Subject))
		}
		for _, s := range dm.Attachments {
			rel, _ := filepath.Rel(abs, s.Path)
			fmt.Fprintf(&md, "- Attachment: [%s](%s) (%s)\n", mdEscapeLine(s.Filename), filepath.ToSlash(rel), humanSize(int64(s.Size)))
		}
		fence := codeFence(text)
		fmt.Fprintf(&md, "\n%stext\n%s\n%s\n", fence, strings.TrimRight(text, "\n"), fence)
	}
	if err := os.WriteFile(man.Transcript, []byte(md.String()), 0o644); err != nil {
		return nil, err
	}
	js, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(abs, "thread.json"), js, 0o644); err != nil {
		return nil, err
	}
	return man, nil
}

// codeFence returns a backtick fence longer than any backtick run in text.
func codeFence(text string) string {
	longest, run := 0, 0
	for _, r := range text {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}

func mdEscapeLine(s string) string {
	return strings.NewReplacer("<", "&lt;", ">", "&gt;", "\n", " ").Replace(s)
}
