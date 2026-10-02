// Package cli implements gomail's command-line interface.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"gomail/internal/config"
	"gomail/internal/gmail"
	"gomail/internal/smtpsend"
)

// app carries global flags and lazily loaded state shared by every command.
type app struct {
	cfgPath     string
	profileName string
	jsonOut     bool
	debug       bool

	out    io.Writer
	errOut io.Writer
	in     *os.File

	cfg         *config.Config
	profileKey  string
	prof        *config.Profile
	imapClient  *gmail.Client
	warnedPerms bool
}

// Execute runs the CLI and returns the process exit code.
func Execute(version string) int {
	a := &app{out: os.Stdout, errOut: os.Stderr, in: os.Stdin}
	root := a.rootCmd(version)
	err := root.Execute()
	a.closeIMAP()
	if err == nil {
		return 0
	}
	a.reportError(err)
	var ue usageError
	if errors.As(err, &ue) {
		return 2
	}
	return 1
}

type usageError struct{ error }

func usageErrorf(format string, args ...any) error {
	return usageError{fmt.Errorf(format, args...)}
}

func (a *app) reportError(err error) {
	if a.jsonOut {
		_ = json.NewEncoder(a.out).Encode(map[string]string{"error": err.Error()})
		return
	}
	fmt.Fprintln(a.errOut, "gomail: "+err.Error())
}

func (a *app) rootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:   "gomail",
		Short: "Search, read, download, draft and send Gmail from the command line",
		Long: `gomail talks to Gmail over IMAP and SMTP using app passwords, with one
profile per account and any number of named signatures per profile.

Message and thread ids are Gmail's own hex ids (the same ones the Gmail web UI
uses). Search uses Gmail's search syntax (from:, to:, subject:, has:attachment,
in:sent, label:, after:2026/01/01, is:unread, ...).

Every command accepts --json for machine-readable output.`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return usageError{err}
	})
	pf := root.PersistentFlags()
	pf.StringVarP(&a.profileName, "profile", "p", "", "account profile to use (name or email; default: $GOMAIL_PROFILE or default_profile)")
	pf.StringVar(&a.cfgPath, "config", "", "config file (default: $GOMAIL_CONFIG or ~/.config/gomail/config.yaml)")
	pf.BoolVar(&a.jsonOut, "json", false, "print machine-readable JSON")
	pf.BoolVar(&a.debug, "debug", false, "trace IMAP/SMTP traffic to stderr (passwords redacted)")

	root.AddCommand(
		a.profileCmd(),
		a.signatureCmd(),
		a.configCmd(),
		a.labelsCmd(),
		a.labelCmd(),
		a.searchCmd(),
		a.listCmd(),
		a.readCmd(),
		a.threadCmd(),
		a.attachmentsCmd(),
		a.downloadCmd(),
		a.sendCmd(),
		a.previewCmd(),
		a.replyCmd(),
		a.forwardCmd(),
		a.draftCmd(),
		a.modifyCmd(),
		a.archiveCmd(),
		a.trashCmd(),
	)
	return root
}

// config loads the configuration once.
func (a *app) config() (*config.Config, error) {
	if a.cfg != nil {
		return a.cfg, nil
	}
	cfg, err := config.Load(a.cfgPath)
	if err != nil {
		return nil, err
	}
	a.cfg = cfg
	if cfg.InsecurePermissions() && !a.warnedPerms {
		a.warnedPerms = true
		fmt.Fprintf(a.errOut, "gomail: warning: %s contains a password but is readable by others; run: chmod 600 %s\n", cfg.Path(), cfg.Path())
	}
	return cfg, nil
}

// profile resolves the active profile.
func (a *app) profile() (*config.Profile, error) {
	if a.prof != nil {
		return a.prof, nil
	}
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	name, p, err := cfg.Resolve(a.profileName)
	if err != nil {
		return nil, err
	}
	a.profileKey, a.prof = name, p
	return p, nil
}

func (a *app) debugWriter() io.Writer {
	if a.debug {
		return a.errOut
	}
	return nil
}

// imap returns a connected client for the active profile, reused for the
// rest of the command.
func (a *app) imap() (*gmail.Client, error) {
	if a.imapClient != nil {
		return a.imapClient, nil
	}
	p, err := a.profile()
	if err != nil {
		return nil, err
	}
	pw, err := p.ResolvePassword()
	if err != nil {
		return nil, err
	}
	c, err := gmail.Dial(gmail.Config{
		Host:     p.IMAP.Host,
		Port:     p.IMAP.Port,
		Username: p.Login(),
		Password: pw,
		Timeout:  2 * time.Minute,
		Debug:    a.debugWriter(),
	})
	if err != nil {
		return nil, err
	}
	a.imapClient = c
	return c, nil
}

func (a *app) closeIMAP() {
	if a.imapClient != nil {
		_ = a.imapClient.Close()
		a.imapClient = nil
	}
}

func (a *app) smtpConfig() (smtpsend.Config, error) {
	p, err := a.profile()
	if err != nil {
		return smtpsend.Config{}, err
	}
	pw, err := p.ResolvePassword()
	if err != nil {
		return smtpsend.Config{}, err
	}
	return smtpsend.Config{
		Host:     p.SMTP.Host,
		Port:     p.SMTP.Port,
		Username: p.Login(),
		Password: pw,
		Timeout:  2 * time.Minute,
		Debug:    a.debugWriter(),
	}, nil
}

// printJSON writes v as indented JSON.
func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func (a *app) printf(format string, args ...any) {
	fmt.Fprintf(a.out, format, args...)
}

// note prints progress or hints to stderr so stdout stays clean for data.
func (a *app) note(format string, args ...any) {
	fmt.Fprintf(a.errOut, format+"\n", args...)
}

// isSelf reports whether email belongs to the active profile.
func (a *app) isSelf(email string) bool {
	p, err := a.profile()
	if err != nil {
		return false
	}
	for _, addr := range p.Addresses() {
		if strings.EqualFold(addr, email) {
			return true
		}
	}
	return false
}
