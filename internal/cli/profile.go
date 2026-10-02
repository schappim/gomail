package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"gomail/internal/config"
	"gomail/internal/smtpsend"
)

func (a *app) profileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "profile",
		Aliases: []string{"profiles", "account", "accounts"},
		Short:   "Manage account profiles (one per Gmail account)",
	}
	cmd.AddCommand(a.profileListCmd(), a.profileAddCmd(), a.profileRemoveCmd(), a.profileDefaultCmd(), a.profileTestCmd())
	return cmd
}

type profileInfo struct {
	Name             string   `json:"name"`
	Email            string   `json:"email"`
	DisplayName      string   `json:"display_name,omitempty"`
	Default          bool     `json:"default"`
	PasswordSource   string   `json:"password_source"`
	Aliases          []string `json:"aliases,omitempty"`
	Signatures       []string `json:"signatures"`
	DefaultSignature string   `json:"default_signature,omitempty"`
}

func passwordSource(p *config.Profile) string {
	switch {
	case p.PasswordEnv != "":
		return "env:" + p.PasswordEnv
	case p.PasswordCmd != "":
		return "cmd"
	case p.Password != "":
		return "config"
	}
	return "none"
}

func (a *app) profileListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List configured profiles",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			defName, _, _ := cfg.Resolve("")
			var infos []profileInfo
			for _, name := range cfg.ProfileNames() {
				p := cfg.Profiles[name]
				infos = append(infos, profileInfo{
					Name: name, Email: p.Email, DisplayName: p.Name, Default: name == defName,
					PasswordSource: passwordSource(p), Aliases: p.Aliases,
					Signatures: p.SignatureNames(), DefaultSignature: p.DefaultSignature,
				})
			}
			if a.jsonOut {
				if infos == nil {
					infos = []profileInfo{}
				}
				return a.printJSON(map[string]any{"config": cfg.Path(), "profiles": infos})
			}
			if len(infos) == 0 {
				a.printf("No profiles yet (config: %s).\nAdd one with: gomail profile add personal --email you@gmail.com --name \"Your Name\"\n", cfg.Path())
				return nil
			}
			for _, in := range infos {
				mark := " "
				if in.Default {
					mark = "*"
				}
				sigs := "no signatures"
				if len(in.Signatures) > 0 {
					sigs = "signatures: " + strings.Join(in.Signatures, ", ")
					if in.DefaultSignature != "" {
						sigs += " (default " + in.DefaultSignature + ")"
					}
				}
				a.printf("%s %-12s %-32s password:%-8s %s\n", mark, in.Name, in.Email, in.PasswordSource, sigs)
			}
			return nil
		},
	}
}

func (a *app) profileAddCmd() *cobra.Command {
	var email, name, username, pwEnv, pwCmd, imapHost, smtpHost string
	var imapPort, smtpPort int
	var pwStdin, askPw, makeDefault bool
	var aliases []string
	cmd := &cobra.Command{
		Use:   "add NAME --email ADDRESS",
		Short: "Add or update a profile",
		Long: `Add or update an account profile.

Create an app password at https://myaccount.google.com/apppasswords (2-Step
Verification must be on, and IMAP enabled in Gmail settings). Supply it one of
these ways:
  (interactive)        you are prompted, input hidden, stored in the config
                       (an existing password is kept; --ask-password replaces it)
  --password-stdin     read it from stdin, stored in the config
  --password-env VAR   read from $VAR at run time, nothing stored
  --password-cmd CMD   run CMD at run time (e.g. a keychain lookup), nothing stored`,
		Example: `  gomail profile add personal --email me@gmail.com --name "Alex Example" --default
  echo "abcd efgh ijkl mnop" | gomail profile add work --email me@company.com --password-stdin
  gomail profile add shop --email shop@example.com --password-cmd "security find-generic-password -s gomail-shop -w"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			key := args[0]
			p, exists := cfg.Profiles[key]
			if !exists {
				if email == "" {
					return usageErrorf("--email is required for a new profile")
				}
				p = &config.Profile{}
				cfg.Profiles[key] = p
			}
			if email != "" {
				p.Email = email
			}
			if cmd.Flags().Changed("name") {
				p.Name = name
			}
			if cmd.Flags().Changed("username") {
				p.Username = username
			}
			if cmd.Flags().Changed("alias") {
				p.Aliases = aliases
			}
			if imapHost != "" {
				p.IMAP.Host = imapHost
			}
			if imapPort != 0 {
				p.IMAP.Port = imapPort
			}
			if smtpHost != "" {
				p.SMTP.Host = smtpHost
			}
			if smtpPort != 0 {
				p.SMTP.Port = smtpPort
			}
			switch {
			case pwEnv != "":
				p.PasswordEnv, p.PasswordCmd, p.Password = pwEnv, "", ""
			case pwCmd != "":
				p.PasswordEnv, p.PasswordCmd, p.Password = "", pwCmd, ""
			case pwStdin:
				pw, err := readLine(a.in)
				if err != nil {
					return fmt.Errorf("reading password from stdin: %w", err)
				}
				p.PasswordEnv, p.PasswordCmd, p.Password = "", "", pw
			case exists && !askPw && (p.Password != "" || p.PasswordEnv != "" || p.PasswordCmd != ""):
				// keep the existing password source
			default:
				if !isTerminal(a.in) {
					return usageErrorf("no password given: use --password-stdin, --password-env or --password-cmd")
				}
				fmt.Fprintf(a.errOut, "App password for %s (input hidden): ", p.Email)
				b, err := term.ReadPassword(int(a.in.Fd()))
				fmt.Fprintln(a.errOut)
				if err != nil {
					return err
				}
				p.Password = strings.TrimSpace(string(b))
				p.PasswordEnv, p.PasswordCmd = "", ""
			}
			if p.Password != "" {
				p.Password = strings.ReplaceAll(p.Password, " ", "")
			}
			if makeDefault || len(cfg.Profiles) == 1 {
				cfg.DefaultProfile = key
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(map[string]any{"saved": key, "email": p.Email, "config": cfg.Path()})
			}
			verb := "Added"
			if exists {
				verb = "Updated"
			}
			a.printf("%s profile %q (%s) in %s\n", verb, key, p.Email, cfg.Path())
			a.note("Check the connection with: gomail profile test %s", key)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&email, "email", "", "Gmail address")
	f.StringVar(&name, "name", "", "display name for the From header")
	f.StringVar(&username, "username", "", "login name if different from the email")
	f.StringArrayVar(&aliases, "alias", nil, "additional send-as address (repeatable)")
	f.BoolVar(&pwStdin, "password-stdin", false, "read the app password from stdin")
	f.BoolVar(&askPw, "ask-password", false, "prompt for a new app password even if one is already set")
	f.StringVar(&pwEnv, "password-env", "", "read the app password from this environment variable at run time")
	f.StringVar(&pwCmd, "password-cmd", "", "shell command that prints the app password")
	f.StringVar(&imapHost, "imap-host", "", "IMAP host (default imap.gmail.com)")
	f.IntVar(&imapPort, "imap-port", 0, "IMAP port (default 993)")
	f.StringVar(&smtpHost, "smtp-host", "", "SMTP host (default smtp.gmail.com)")
	f.IntVar(&smtpPort, "smtp-port", 0, "SMTP port (default 465; 587 uses STARTTLS)")
	f.BoolVar(&makeDefault, "default", false, "make this the default profile")
	return cmd
}

func readLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New("empty input")
	}
	return line, nil
}

func (a *app) profileRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove NAME",
		Aliases: []string{"rm", "delete"},
		Short:   "Remove a profile",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			if _, ok := cfg.Profiles[args[0]]; !ok {
				return fmt.Errorf("unknown profile %q", args[0])
			}
			delete(cfg.Profiles, args[0])
			if cfg.DefaultProfile == args[0] {
				cfg.DefaultProfile = ""
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(map[string]string{"removed": args[0]})
			}
			a.printf("Removed profile %q\n", args[0])
			return nil
		},
	}
}

func (a *app) profileDefaultCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "default NAME",
		Short: "Set the default profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			key, _, err := cfg.Resolve(args[0])
			if err != nil {
				return err
			}
			cfg.DefaultProfile = key
			if err := cfg.Save(); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(map[string]string{"default_profile": key})
			}
			a.printf("Default profile is now %q\n", key)
			return nil
		},
	}
}

func (a *app) profileTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test [NAME]",
		Short: "Check that IMAP and SMTP logins work",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				a.profileName = args[0]
			}
			p, err := a.profile()
			if err != nil {
				return err
			}
			result := map[string]any{"profile": a.profileKey, "email": p.Email}
			var failed []string

			c, err := a.imap()
			if err == nil {
				var n int
				labels, lerr := c.Labels(false)
				err, n = lerr, len(labels)
				if err == nil {
					result["imap"] = fmt.Sprintf("ok (%d labels)", n)
				}
			}
			if err != nil {
				result["imap"] = err.Error()
				failed = append(failed, "IMAP")
			}

			sc, err := a.smtpConfig()
			if err == nil {
				err = smtpsend.Check(sc)
			}
			if err != nil {
				result["smtp"] = err.Error()
				failed = append(failed, "SMTP")
			} else {
				result["smtp"] = "ok"
			}

			if a.jsonOut {
				result["ok"] = len(failed) == 0
				if err := a.printJSON(result); err != nil {
					return err
				}
			} else {
				a.printf("Profile %s (%s)\n  IMAP: %v\n  SMTP: %v\n", a.profileKey, p.Email, result["imap"], result["smtp"])
			}
			if len(failed) > 0 {
				return fmt.Errorf("%s check failed", strings.Join(failed, " and "))
			}
			return nil
		},
	}
}

func (a *app) configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show the config file location or create a commented example",
		Long: `gomail reads a YAML config (default ~/.config/gomail/config.yaml, override
with --config or $GOMAIL_CONFIG):

` + config.Example,
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "Print the config file path",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := a.cfgPath
			if path == "" {
				path = config.DefaultPath()
			}
			if a.jsonOut {
				return a.printJSON(map[string]string{"config": path})
			}
			a.printf("%s\n", path)
			return nil
		},
	}, &cobra.Command{
		Use:   "init",
		Short: "Write a commented example config (refuses to overwrite)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := a.cfgPath
			if path == "" {
				path = config.DefaultPath()
			}
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s already exists", path)
			}
			if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(config.Example), 0o600); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(map[string]string{"created": path})
			}
			a.printf("Wrote example config to %s. Edit it, then run: gomail profile test\n", path)
			return nil
		},
	})
	return cmd
}
