package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"gomail/internal/compose"
	"gomail/internal/model"
	"gomail/internal/smtpsend"
)

// composeFlags are the flags shared by send, preview, draft, reply and forward.
type composeFlags struct {
	to, cc, bcc  []string
	replyTo      string
	from         string
	subject      string
	body         string
	bodyFile     string
	html         string
	htmlFile     string
	markdown     string
	markdownFile string
	attach       []string
	inline       []string
	signature    string
	noSignature  bool

	// output and confirmation
	yes      bool
	open     bool
	showHTML bool
	raw      bool
	emlPath  string
}

func (cf *composeFlags) registerMessage(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringArrayVar(&cf.to, "to", nil, "recipient (repeatable; \"Name <a@b.com>\" or comma-separated)")
	f.StringArrayVar(&cf.cc, "cc", nil, "Cc recipient (repeatable)")
	f.StringArrayVar(&cf.bcc, "bcc", nil, "Bcc recipient (repeatable)")
	f.StringVar(&cf.replyTo, "reply-to", "", "Reply-To address(es)")
	f.StringVar(&cf.from, "from", "", "From address (default: the profile's name and email; must be a Gmail send-as alias)")
	f.StringVarP(&cf.subject, "subject", "s", "", "subject")
	f.StringVarP(&cf.body, "body", "b", "", "plain-text body")
	f.StringVar(&cf.bodyFile, "body-file", "", "read the plain-text body from a file (- for stdin)")
	f.StringVar(&cf.html, "html", "", "HTML body (a text version is generated unless --body is also given)")
	f.StringVar(&cf.htmlFile, "html-file", "", "read the HTML body from a file (- for stdin)")
	f.StringVarP(&cf.markdown, "markdown", "m", "", "Markdown body, sent as HTML plus a plain-text version")
	f.StringVar(&cf.markdownFile, "markdown-file", "", "read the Markdown body from a file (- for stdin)")
	f.StringArrayVarP(&cf.attach, "attach", "a", nil, "attach a file (repeatable)")
	f.StringArrayVar(&cf.inline, "inline", nil, "embed an image for the HTML body, referenced as <img src=\"cid:FILENAME\"> (repeatable)")
	f.StringVarP(&cf.signature, "signature", "S", "", "signature name (default: the profile's default signature)")
	f.BoolVar(&cf.noSignature, "no-signature", false, "don't add a signature")
}

func (cf *composeFlags) registerPreview(cmd *cobra.Command) {
	f := cmd.Flags()
	f.BoolVar(&cf.open, "open", false, "open the HTML version in a browser")
	f.BoolVar(&cf.showHTML, "show-html", false, "print the HTML part in the preview")
	f.BoolVar(&cf.raw, "raw", false, "print the complete raw MIME message")
	f.StringVar(&cf.emlPath, "eml", "", "also save the raw message to this .eml file")
}

func (cf *composeFlags) hasBody() bool {
	return cf.body != "" || cf.bodyFile != "" || cf.html != "" || cf.htmlFile != "" || cf.markdown != "" || cf.markdownFile != ""
}

// readSource returns inline, or the contents of file ("-" for stdin).
func (a *app) readSource(flag, inline, file string) (string, error) {
	if inline != "" && file != "" {
		return "", usageErrorf("give --%s or --%s-file, not both", flag, flag)
	}
	if file == "" {
		return inline, nil
	}
	var b []byte
	var err error
	if file == "-" {
		b, err = io.ReadAll(a.in)
	} else {
		b, err = os.ReadFile(file)
	}
	if err != nil {
		return "", fmt.Errorf("reading --%s-file: %w", flag, err)
	}
	return string(b), nil
}

func parseAddressFlags(values []string) ([]model.Address, error) {
	var out []model.Address
	for _, v := range values {
		list, err := compose.ParseAddressList(v)
		if err != nil {
			return nil, err
		}
		out = append(out, list...)
	}
	return out, nil
}

// applyFlags layers the command-line flags over base (which carries reply or
// forward defaults) and returns the options plus the chosen signature name.
func (a *app) applyFlags(cf *composeFlags, base compose.Options) (compose.Options, string, error) {
	o := base
	cfg, err := a.config()
	if err != nil {
		return o, "", err
	}
	p, err := a.profile()
	if err != nil {
		return o, "", err
	}

	o.From = model.Address{Name: p.Name, Email: p.Email}
	if cf.from != "" {
		list, err := compose.ParseAddressList(cf.from)
		if err != nil {
			return o, "", err
		}
		if len(list) != 1 {
			return o, "", usageErrorf("--from takes exactly one address")
		}
		o.From = list[0]
		if o.From.Name == "" {
			o.From.Name = p.Name
		}
		if !a.isSelf(o.From.Email) {
			a.note("warning: %s is not this profile's address or a configured alias; Gmail rewrites From unless it is a verified send-as address", o.From.Email)
		}
	}

	for _, rc := range []struct {
		dst  *[]model.Address
		vals []string
	}{{&o.To, cf.to}, {&o.Cc, cf.cc}, {&o.Bcc, cf.bcc}} {
		list, err := parseAddressFlags(rc.vals)
		if err != nil {
			return o, "", err
		}
		*rc.dst = append(*rc.dst, list...)
	}
	if cf.replyTo != "" {
		if o.ReplyTo, err = compose.ParseAddressList(cf.replyTo); err != nil {
			return o, "", err
		}
	}
	if cf.subject != "" {
		o.Subject = cf.subject
	}

	if o.Text, err = a.readSource("body", cf.body, cf.bodyFile); err != nil {
		return o, "", err
	}
	if o.HTML, err = a.readSource("html", cf.html, cf.htmlFile); err != nil {
		return o, "", err
	}
	if o.Markdown, err = a.readSource("markdown", cf.markdown, cf.markdownFile); err != nil {
		return o, "", err
	}
	if o.Markdown != "" && (o.Text != "" || o.HTML != "") {
		return o, "", usageErrorf("--markdown can't be combined with --body or --html")
	}

	for _, path := range cf.attach {
		f, err := compose.LoadFile(expandHome(path))
		if err != nil {
			return o, "", err
		}
		o.Attachments = append(o.Attachments, f)
	}
	for _, path := range cf.inline {
		f, err := compose.LoadFile(expandHome(path))
		if err != nil {
			return o, "", err
		}
		f.Inline = true
		f.ContentID = f.Filename
		o.Attachments = append(o.Attachments, f)
	}

	sigName := ""
	if !cf.noSignature {
		rs, err := cfg.Signature(p, cf.signature)
		if err != nil {
			return o, "", err
		}
		if rs != nil {
			o.Signature = &compose.Signature{Name: rs.Name, Text: rs.Text, HTML: rs.HTML}
			sigName = rs.Name
		}
	}
	return o, sigName, nil
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// previewJSON is the JSON shape of a composed (not yet sent) message.
type previewJSON struct {
	Action      string             `json:"action"`
	Profile     string             `json:"profile"`
	From        model.Address      `json:"from"`
	To          []model.Address    `json:"to"`
	Cc          []model.Address    `json:"cc,omitempty"`
	Bcc         []model.Address    `json:"bcc,omitempty"`
	ReplyTo     []model.Address    `json:"reply_to,omitempty"`
	Subject     string             `json:"subject"`
	Signature   string             `json:"signature,omitempty"`
	InReplyTo   string             `json:"in_reply_to,omitempty"`
	MessageID   string             `json:"message_id"`
	Structure   string             `json:"structure"`
	Size        int                `json:"size"`
	Recipients  []string           `json:"recipients"`
	Attachments []model.Attachment `json:"attachments"`
	Text        string             `json:"text"`
	HTML        string             `json:"html,omitempty"`
	EML         string             `json:"eml,omitempty"`
	HTMLFile    string             `json:"html_file,omitempty"`
}

func (a *app) previewData(action string, o compose.Options, b *compose.Built, sig string) previewJSON {
	atts := b.Attachments
	if atts == nil {
		atts = []model.Attachment{}
	}
	to := o.To
	if to == nil {
		to = []model.Address{}
	}
	recips := b.Recipients
	if recips == nil {
		recips = []string{}
	}
	return previewJSON{
		Action: action, Profile: a.profileKey, From: o.From, To: to, Cc: o.Cc, Bcc: o.Bcc, ReplyTo: o.ReplyTo,
		Subject: o.Subject, Signature: sig, InReplyTo: o.InReplyTo, MessageID: b.MessageID,
		Structure: b.Structure, Size: len(b.Raw), Recipients: recips, Attachments: atts,
		Text: b.Text, HTML: b.HTML,
	}
}

// showPreview prints a composed message for review and handles --open,
// --eml and --raw.
func (a *app) showPreview(cf *composeFlags, pv *previewJSON, b *compose.Built, inline []model.Attachment, toStderr bool) error {
	if cf.emlPath != "" {
		if err := os.WriteFile(expandHome(cf.emlPath), b.Raw, 0o644); err != nil {
			return err
		}
		pv.EML = cf.emlPath
	}
	if cf.open && b.HTML != "" {
		path, err := writeTempHTML("gomail-preview", inlineCIDs(b.HTML, inline))
		if err != nil {
			return err
		}
		pv.HTMLFile = path
		if err := openInBrowser(path); err != nil {
			return err
		}
	}
	if a.jsonOut && !toStderr {
		return a.printJSON(pv)
	}
	w := a.out
	if toStderr {
		w = a.errOut
	}
	if cf.raw {
		_, err := w.Write(b.Raw)
		return err
	}
	pr := func(format string, args ...any) { fmt.Fprintf(w, format, args...) }
	pr("From:      %s\n", pv.From.String())
	pr("To:        %s\n", model.FormatAddresses(pv.To))
	if len(pv.Cc) > 0 {
		pr("Cc:        %s\n", model.FormatAddresses(pv.Cc))
	}
	if len(pv.Bcc) > 0 {
		pr("Bcc:       %s\n", model.FormatAddresses(pv.Bcc))
	}
	if len(pv.ReplyTo) > 0 {
		pr("Reply-To:  %s\n", model.FormatAddresses(pv.ReplyTo))
	}
	pr("Subject:   %s\n", pv.Subject)
	if pv.InReplyTo != "" {
		pr("In-Reply-To: <%s>\n", pv.InReplyTo)
	}
	sig := pv.Signature
	if sig == "" {
		sig = "(none)"
	}
	pr("Signature: %s\n", sig)
	for _, att := range pv.Attachments {
		kind := "attachment"
		if att.Inline {
			kind = "inline"
		}
		pr("%-10s %s (%s, %s)\n", strings.ToUpper(kind[:1])+kind[1:]+":", att.Filename, att.ContentType, humanSize(int64(att.Size)))
	}
	pr("Format:    %s, %s\n", pv.Structure, humanSize(int64(pv.Size)))
	if pv.EML != "" {
		pr("Saved:     %s\n", pv.EML)
	}
	const tick = "───"
	if b.Text != "" {
		pr("%s text/plain\n%s\n", tick, strings.TrimRight(b.Text, "\n"))
	}
	if b.HTML != "" {
		switch {
		case cf.showHTML:
			pr("%s text/html\n%s\n", tick, b.HTML)
		case pv.HTMLFile != "":
			pr("%s text/html opened in browser: %s\n", tick, pv.HTMLFile)
		default:
			pr("%s text/html (%s) — use --show-html to print it or --open to view it\n", tick, humanSize(int64(len(b.HTML))))
		}
	}
	pr("%s\n", strings.Repeat("─", 60))
	return nil
}

// confirm asks the user before sending unless --yes was given. Without a
// terminal it refuses, so scripts and agents must opt in explicitly. show
// prints what is about to be sent.
func (a *app) confirm(cf *composeFlags, question string, show func() error) error {
	if cf.yes {
		return nil
	}
	if !isTerminal(a.in) || a.jsonOut {
		return errors.New("refusing to send without --yes (review it first with `gomail preview` or the same command without --send)")
	}
	if err := show(); err != nil {
		return err
	}
	fmt.Fprintf(a.errOut, "%s [y/N] ", question)
	line, _ := bufio.NewReader(a.in).ReadString('\n')
	if ans := strings.ToLower(strings.TrimSpace(line)); ans != "y" && ans != "yes" {
		return errors.New("cancelled, nothing sent")
	}
	return nil
}

// deliver sends a built message over SMTP after confirmation.
func (a *app) deliver(cf *composeFlags, pv *previewJSON, b *compose.Built, inline []model.Attachment) error {
	if len(b.Recipients) == 0 {
		return usageErrorf("no recipients: add --to")
	}
	show := func() error { return a.showPreview(cf, pv, b, inline, true) }
	if err := a.confirm(cf, fmt.Sprintf("Send to %s?", strings.Join(b.Recipients, ", ")), show); err != nil {
		return err
	}
	sc, err := a.smtpConfig()
	if err != nil {
		return err
	}
	p, _ := a.profile()
	if err := smtpsend.Send(sc, p.Email, b.Recipients, b.Raw); err != nil {
		return err
	}
	if a.jsonOut {
		return a.printJSON(map[string]any{
			"sent": true, "profile": a.profileKey, "message_id": b.MessageID,
			"recipients": b.Recipients, "subject": pv.Subject,
		})
	}
	a.printf("Sent to %s (Message-ID <%s>). Gmail files it in Sent; find it with: gomail search rfc822msgid:%s\n", strings.Join(b.Recipients, ", "), b.MessageID, b.MessageID)
	return nil
}

// saveDraft stores a built message as a Gmail draft.
func (a *app) saveDraft(pv *previewJSON, b *compose.Built) error {
	c, err := a.imap()
	if err != nil {
		return err
	}
	sum, err := c.AppendDraft(b.Raw)
	if err != nil {
		return err
	}
	if a.jsonOut {
		return a.printJSON(map[string]any{
			"draft": true, "profile": a.profileKey, "id": sum.ID, "thread_id": sum.ThreadID,
			"message_id": b.MessageID, "subject": pv.Subject, "recipients": b.Recipients,
		})
	}
	a.printf("Saved draft %s (thread %s): %s\n", sum.ID, sum.ThreadID, pv.Subject)
	a.note("Review: gomail draft show %s   Send: gomail draft send %s", sum.ID, sum.ID)
	return nil
}

// composeAction is what to do with a composed message.
type composeAction int

const (
	actionPreview composeAction = iota
	actionDraft
	actionSend
)

func (a *app) runCompose(cf *composeFlags, base compose.Options, act composeAction) error {
	o, sig, err := a.applyFlags(cf, base)
	if err != nil {
		return err
	}
	if act == actionSend && len(o.To)+len(o.Cc)+len(o.Bcc) == 0 {
		return usageErrorf("no recipients: add --to")
	}
	o.IncludeBcc = act != actionSend
	b, err := compose.Build(o)
	if err != nil {
		return err
	}
	names := map[composeAction]string{actionPreview: "preview", actionDraft: "draft", actionSend: "send"}
	pv := a.previewData(names[act], o, b, sig)
	inline := inlineImages(o.Attachments)
	switch act {
	case actionDraft:
		return a.saveDraft(&pv, b)
	case actionSend:
		return a.deliver(cf, &pv, b, inline)
	}
	return a.showPreview(cf, &pv, b, inline, false)
}

// inlineImages returns the cid-referenced files so a browser preview can show them.
func inlineImages(files []compose.File) []model.Attachment {
	var out []model.Attachment
	for _, f := range files {
		if f.ContentID != "" {
			out = append(out, model.Attachment{Filename: f.Filename, ContentType: f.ContentType, ContentID: f.ContentID, Data: f.Data})
		}
	}
	return out
}

const composeHelp = `
Bodies: --body/--body-file for plain text, --html/--html-file for HTML (a
plain-text alternative is generated), --markdown/--markdown-file for Markdown
(sent as HTML plus the Markdown source as text). Text-only messages stay
text-only. The profile's default signature is appended unless --signature NAME
or --no-signature is given.`

func (a *app) sendCmd() *cobra.Command {
	var cf composeFlags
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Compose and send a new message",
		Long: `Compose and send a new message through Gmail's SMTP server. Gmail files a
copy in Sent automatically.

You are shown a preview and asked to confirm. Without a terminal (scripts,
agents) --yes is required.` + composeHelp,
		Example: `  gomail send --to jane@example.com -s "Quote" --body-file quote.txt --attach quote.pdf
  gomail -p personal send --to customer@example.com -s Hi -m "**Thanks** for your order" -S no-phone --yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			act := actionSend
			if dryRun {
				act = actionPreview
			}
			return a.runCompose(&cf, compose.Options{}, act)
		},
	}
	cf.registerMessage(cmd)
	cf.registerPreview(cmd)
	cmd.Flags().BoolVarP(&cf.yes, "yes", "y", false, "send without asking for confirmation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "only preview, don't send")
	return cmd
}

func (a *app) previewCmd() *cobra.Command {
	var cf composeFlags
	cmd := &cobra.Command{
		Use:   "preview",
		Short: "Compose a message and show exactly what would be sent, without sending",
		Long:  `Build the message (bodies, signature, attachments) and show it. Nothing is sent or saved.` + composeHelp,
		Example: `  gomail preview --to jane@example.com -s Hello -m "Hi *Jane*" --open
  gomail preview --to x@y.com -s Test --html-file mail.html --eml /tmp/test.eml --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runCompose(&cf, compose.Options{}, actionPreview)
		},
	}
	cf.registerMessage(cmd)
	cf.registerPreview(cmd)
	return cmd
}

func actionFromFlags(send, draft bool) (composeAction, error) {
	switch {
	case send && draft:
		return 0, usageErrorf("choose --send or --draft, not both")
	case send:
		return actionSend, nil
	case draft:
		return actionDraft, nil
	}
	return actionPreview, nil
}

func (a *app) replyCmd() *cobra.Command {
	var cf composeFlags
	var all, send, draft bool
	cmd := &cobra.Command{
		Use:   "reply ID",
		Short: "Reply to a message (preview by default; --draft or --send)",
		Long: `Reply to a message, threaded correctly in Gmail (In-Reply-To/References,
"Re:" subject, original quoted below your signature). Replies go to the
Reply-To or sender; --all also copies the other recipients, leaving out your
own addresses. --to/--cc/--bcc add extra recipients.

Without --draft or --send this only previews the reply.` + composeHelp,
		Example: `  gomail reply 18c3f2a1b2c3d4e5 -b "Thanks, received." --draft
  gomail -p personal reply 18c3f2a1b2c3d4e5 --all -m "Sounds good — see you **Monday**." -S no-phone --send --yes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			act, err := actionFromFlags(send, draft)
			if err != nil {
				return err
			}
			if act != actionPreview && !cf.hasBody() {
				return usageErrorf("a reply needs a body: --body, --html or --markdown")
			}
			orig, err := a.fetchMessage(args[0])
			if err != nil {
				return err
			}
			p, err := a.profile()
			if err != nil {
				return err
			}
			return a.runCompose(&cf, compose.Reply(orig, p.Addresses(), all), act)
		},
	}
	cf.registerMessage(cmd)
	cf.registerPreview(cmd)
	f := cmd.Flags()
	f.BoolVar(&all, "all", false, "reply to all recipients")
	f.BoolVar(&send, "send", false, "send the reply")
	f.BoolVar(&draft, "draft", false, "save the reply as a draft in the thread")
	f.BoolVarP(&cf.yes, "yes", "y", false, "with --send: don't ask for confirmation")
	return cmd
}

func (a *app) forwardCmd() *cobra.Command {
	var cf composeFlags
	var noAttachments, send, draft bool
	cmd := &cobra.Command{
		Use:     "forward ID --to ADDRESS",
		Aliases: []string{"fwd"},
		Short:   "Forward a message with its attachments (preview by default; --draft or --send)",
		Long:    `Forward a message, including its attachments unless --no-attachments is given. Without --draft or --send this only previews.` + composeHelp,
		Example: `  gomail forward 18c3f2a1b2c3d4e5 --to accounts@example.com -b "FYI, invoice attached." --send`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			act, err := actionFromFlags(send, draft)
			if err != nil {
				return err
			}
			orig, err := a.fetchMessage(args[0])
			if err != nil {
				return err
			}
			return a.runCompose(&cf, compose.Forward(orig, !noAttachments), act)
		},
	}
	cf.registerMessage(cmd)
	cf.registerPreview(cmd)
	f := cmd.Flags()
	f.BoolVar(&noAttachments, "no-attachments", false, "don't include the original attachments")
	f.BoolVar(&send, "send", false, "send it")
	f.BoolVar(&draft, "draft", false, "save it as a draft")
	f.BoolVarP(&cf.yes, "yes", "y", false, "with --send: don't ask for confirmation")
	return cmd
}
