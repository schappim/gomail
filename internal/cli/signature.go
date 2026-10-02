package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"gomail/internal/config"
	"gomail/internal/htmltext"
)

func (a *app) signatureCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "signature",
		Aliases: []string{"signatures", "sig"},
		Short:   "Manage named signatures for the selected profile",
		Long: `Each profile can hold several named signatures, e.g. "full" (with your
mobile) and "no-phone" (for customers). Pick one when composing with
--signature NAME, or --no-signature for none; otherwise the profile's
default_signature is used.`,
	}
	cmd.AddCommand(a.signatureListCmd(), a.signatureShowCmd(), a.signatureSetCmd(), a.signatureRemoveCmd(), a.signatureDefaultCmd())
	return cmd
}

func (a *app) signatureListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List signatures for the profile",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			p, err := a.profile()
			if err != nil {
				return err
			}
			type sigInfo struct {
				Name        string `json:"name"`
				Description string `json:"description,omitempty"`
				Default     bool   `json:"default"`
				HasText     bool   `json:"has_text"`
				HasHTML     bool   `json:"has_html"`
				Preview     string `json:"preview"`
				Error       string `json:"error,omitempty"`
			}
			var list []sigInfo
			for _, name := range p.SignatureNames() {
				info := sigInfo{Name: name, Description: p.Signatures[name].Description, Default: strings.EqualFold(name, p.DefaultSignature)}
				rs, err := cfg.Signature(p, name)
				if err != nil {
					info.Error = err.Error()
				} else {
					info.HasText, info.HasHTML = rs.Text != "", rs.HTML != ""
					text := rs.Text
					if text == "" {
						text = htmltext.Convert(rs.HTML)
					}
					info.Preview = strings.Join(strings.Fields(text), " ")
				}
				list = append(list, info)
			}
			if a.jsonOut {
				if list == nil {
					list = []sigInfo{}
				}
				return a.printJSON(map[string]any{"profile": a.profileKey, "signatures": list})
			}
			if len(list) == 0 {
				a.printf("Profile %s has no signatures. Add one with: gomail signature set NAME --text \"...\"\n", a.profileKey)
				return nil
			}
			a.printf("Signatures for %s (%s):\n", a.profileKey, p.Email)
			for _, s := range list {
				mark := " "
				if s.Default {
					mark = "*"
				}
				desc := s.Description
				if s.Error != "" {
					desc = "ERROR: " + s.Error
				}
				a.printf("%s %-14s %s\n", mark, s.Name, desc)
				if s.Preview != "" {
					a.printf("    %s\n", truncate(s.Preview, termWidth()-6))
				}
			}
			return nil
		},
	}
}

func (a *app) signatureShowCmd() *cobra.Command {
	var open bool
	cmd := &cobra.Command{
		Use:   "show NAME",
		Short: "Show a signature's text and HTML",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			p, err := a.profile()
			if err != nil {
				return err
			}
			rs, err := cfg.Signature(p, args[0])
			if err != nil {
				return err
			}
			if rs == nil {
				return fmt.Errorf("%q is not a signature", args[0])
			}
			if a.jsonOut {
				return a.printJSON(map[string]string{"name": rs.Name, "description": rs.Description, "text": rs.Text, "html": rs.HTML})
			}
			a.printf("Signature %s", rs.Name)
			if rs.Description != "" {
				a.printf(" — %s", rs.Description)
			}
			a.printf("\n\n--- text ---\n%s\n", orDerived(rs.Text, rs.HTML))
			if rs.HTML != "" {
				a.printf("\n--- html ---\n%s\n", rs.HTML)
				if open {
					path, err := writeTempHTML("gomail-signature", "<!DOCTYPE html><meta charset=\"utf-8\">"+rs.HTML)
					if err != nil {
						return err
					}
					return openInBrowser(path)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&open, "open", false, "open the HTML version in a browser")
	return cmd
}

func orDerived(text, html string) string {
	if text != "" {
		return text
	}
	return htmltext.Convert(html) + "\n(derived from HTML)"
}

func (a *app) signatureSetCmd() *cobra.Command {
	var text, textFile, html, htmlFile, desc string
	var makeDefault, link bool
	cmd := &cobra.Command{
		Use:   "set NAME",
		Short: "Create or replace a signature",
		Long: `Create or replace a named signature on the selected profile. Give a text
version, an HTML version, or both; a missing variant is derived automatically
when composing. File contents are copied into the config unless --link is
given, in which case the config refers to the file.`,
		Example: `  gomail signature set full --text $'Alex Example\nExample Co\n+61 400 000 000' --default
  gomail -p personal signature set no-phone --text "Alex" --description "for customers"
  gomail signature set fancy --html-file ~/sigs/fancy.html --link`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			p, err := a.profile()
			if err != nil {
				return err
			}
			sig := &config.Signature{Description: desc}
			if old, ok := p.Signatures[args[0]]; ok && !cmd.Flags().Changed("description") {
				sig.Description = old.Description
			}
			if sig.Text, sig.TextFile, err = signatureSource(text, textFile, link); err != nil {
				return err
			}
			if sig.HTML, sig.HTMLFile, err = signatureSource(html, htmlFile, link); err != nil {
				return err
			}
			if sig.Text == "" && sig.TextFile == "" && sig.HTML == "" && sig.HTMLFile == "" {
				return usageErrorf("give --text, --text-file, --html or --html-file")
			}
			if p.Signatures == nil {
				p.Signatures = map[string]*config.Signature{}
			}
			p.Signatures[args[0]] = sig
			if makeDefault {
				p.DefaultSignature = args[0]
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(map[string]any{"profile": a.profileKey, "signature": args[0], "default": strings.EqualFold(p.DefaultSignature, args[0])})
			}
			a.printf("Saved signature %q on profile %s\n", args[0], a.profileKey)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&text, "text", "", "plain-text signature")
	f.StringVar(&textFile, "text-file", "", "read the plain-text signature from a file")
	f.StringVar(&html, "html", "", "HTML signature")
	f.StringVar(&htmlFile, "html-file", "", "read the HTML signature from a file")
	f.StringVar(&desc, "description", "", "short note on when to use it")
	f.BoolVar(&makeDefault, "default", false, "make it the profile's default signature")
	f.BoolVar(&link, "link", false, "store file paths instead of copying file contents")
	return cmd
}

// signatureSource returns (inline, file) for a signature variant.
func signatureSource(inline, file string, link bool) (string, string, error) {
	if inline != "" && file != "" {
		return "", "", usageErrorf("give either the inline value or the file, not both")
	}
	if file == "" {
		return inline, "", nil
	}
	if link {
		abs, err := filepath.Abs(file)
		if err != nil {
			return "", "", err
		}
		if _, err := os.Stat(abs); err != nil {
			return "", "", err
		}
		return "", abs, nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", "", err
	}
	return string(b), "", nil
}

func (a *app) signatureRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove NAME",
		Aliases: []string{"rm", "delete"},
		Short:   "Delete a signature",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			p, err := a.profile()
			if err != nil {
				return err
			}
			name, _, ok := p.LookupSignature(args[0])
			if !ok {
				return fmt.Errorf("unknown signature %q", args[0])
			}
			delete(p.Signatures, name)
			if strings.EqualFold(p.DefaultSignature, name) {
				p.DefaultSignature = ""
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(map[string]string{"removed": name})
			}
			a.printf("Removed signature %q from %s\n", name, a.profileKey)
			return nil
		},
	}
}

func (a *app) signatureDefaultCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "default NAME|none",
		Short: "Set the profile's default signature (\"none\" for no default)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			p, err := a.profile()
			if err != nil {
				return err
			}
			name := ""
			if !strings.EqualFold(args[0], "none") {
				n, _, ok := p.LookupSignature(args[0])
				if !ok {
					return fmt.Errorf("unknown signature %q (have: %s)", args[0], strings.Join(p.SignatureNames(), ", "))
				}
				name = n
			}
			p.DefaultSignature = name
			if err := cfg.Save(); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(map[string]string{"profile": a.profileKey, "default_signature": name})
			}
			if name == "" {
				a.printf("Profile %s now has no default signature\n", a.profileKey)
			} else {
				a.printf("Default signature for %s is now %q\n", a.profileKey, name)
			}
			return nil
		},
	}
}

func dirOf(path string) string { return filepath.Dir(path) }
