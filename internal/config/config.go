// Package config loads and saves gomail's YAML configuration: one profile per
// Gmail account (address, app password, server overrides) and any number of
// named signatures per profile.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the whole configuration file.
type Config struct {
	DefaultProfile string              `yaml:"default_profile,omitempty"`
	Profiles       map[string]*Profile `yaml:"profiles"`

	path string
}

// Profile is one Gmail account.
type Profile struct {
	Email    string `yaml:"email"`
	Name     string `yaml:"name,omitempty"`     // display name used in From
	Username string `yaml:"username,omitempty"` // login name, defaults to Email

	// The app password comes from the first of these that is set:
	// password_env (environment variable name), password_cmd (shell command
	// whose stdout is the password, e.g. a keychain lookup), password.
	Password    string `yaml:"password,omitempty"`
	PasswordCmd string `yaml:"password_cmd,omitempty"`
	PasswordEnv string `yaml:"password_env,omitempty"`

	// Aliases are other addresses this account sends as ("Send mail as" in
	// Gmail). They are allowed in --from and left out of reply-all.
	Aliases []string `yaml:"aliases,omitempty"`

	IMAP Server `yaml:"imap,omitempty"`
	SMTP Server `yaml:"smtp,omitempty"`

	DefaultSignature string                `yaml:"default_signature,omitempty"`
	Signatures       map[string]*Signature `yaml:"signatures,omitempty"`
}

// Server overrides a host and port; zero values mean Gmail's defaults.
type Server struct {
	Host string `yaml:"host,omitempty"`
	Port int    `yaml:"port,omitempty"`
}

// Signature is a named signature. Text and HTML may be given inline or as
// files (relative paths are resolved against the config file's directory).
// Either variant may be omitted; the other is derived when composing.
type Signature struct {
	Description string `yaml:"description,omitempty"`
	Text        string `yaml:"text,omitempty"`
	HTML        string `yaml:"html,omitempty"`
	TextFile    string `yaml:"text_file,omitempty"`
	HTMLFile    string `yaml:"html_file,omitempty"`
}

// DefaultPath returns the config path: $GOMAIL_CONFIG, else
// $XDG_CONFIG_HOME/gomail/config.yaml, else ~/.config/gomail/config.yaml.
func DefaultPath() string {
	if p := os.Getenv("GOMAIL_CONFIG"); p != "" {
		return p
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "gomail", "config.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "gomail", "config.yaml")
	}
	return filepath.Join(home, ".config", "gomail", "config.yaml")
}

// Load reads the config at path (DefaultPath when empty). A missing file
// yields an empty config, so first-run commands like "profile add" work.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultPath()
	}
	c := &Config{path: path, Profiles: map[string]*Profile{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if c.Profiles == nil {
		c.Profiles = map[string]*Profile{}
	}
	for name, p := range c.Profiles {
		if p == nil {
			return nil, fmt.Errorf("%s: profile %q is empty", path, name)
		}
		if p.Email == "" {
			return nil, fmt.Errorf("%s: profile %q has no email", path, name)
		}
	}
	return c, nil
}

// Path is the file this config was loaded from and will be saved to.
func (c *Config) Path() string { return c.path }

// Dir is the directory holding the config file.
func (c *Config) Dir() string { return filepath.Dir(c.path) }

// InsecurePermissions reports whether the config file is readable by group
// or others while it holds a plaintext password.
func (c *Config) InsecurePermissions() bool {
	hasPassword := false
	for _, p := range c.Profiles {
		if p.Password != "" {
			hasPassword = true
		}
	}
	if !hasPassword {
		return false
	}
	info, err := os.Stat(c.path)
	return err == nil && info.Mode().Perm()&0o077 != 0
}

// Save writes the config with owner-only permissions.
func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("# gomail configuration. See `gomail help config`.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.path); err != nil {
		return err
	}
	return os.Chmod(c.path, 0o600)
}

// ProfileNames returns profile names sorted alphabetically.
func (c *Config) ProfileNames() []string {
	names := make([]string, 0, len(c.Profiles))
	for n := range c.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Resolve picks a profile: the explicit name (or email address) if given,
// else $GOMAIL_PROFILE, else default_profile, else the only profile.
func (c *Config) Resolve(name string) (string, *Profile, error) {
	if name == "" {
		name = os.Getenv("GOMAIL_PROFILE")
	}
	if name == "" {
		name = c.DefaultProfile
	}
	if name == "" {
		switch len(c.Profiles) {
		case 0:
			return "", nil, fmt.Errorf("no profiles configured: run `gomail profile add NAME --email you@gmail.com` (config: %s)", c.path)
		case 1:
			for n, p := range c.Profiles {
				return n, p, nil
			}
		default:
			return "", nil, fmt.Errorf("several profiles configured (%s): pick one with --profile or set a default with `gomail profile default NAME`", strings.Join(c.ProfileNames(), ", "))
		}
	}
	if p, ok := c.Profiles[name]; ok {
		return name, p, nil
	}
	for n, p := range c.Profiles {
		if strings.EqualFold(p.Email, name) || strings.EqualFold(n, name) {
			return n, p, nil
		}
	}
	return "", nil, fmt.Errorf("unknown profile %q (have: %s)", name, strings.Join(c.ProfileNames(), ", "))
}

// Login returns the IMAP/SMTP username.
func (p *Profile) Login() string {
	if p.Username != "" {
		return p.Username
	}
	return p.Email
}

// ResolvePassword returns the app password with spaces removed (Google shows
// app passwords as four groups of four letters).
func (p *Profile) ResolvePassword() (string, error) {
	var pw string
	switch {
	case p.PasswordEnv != "":
		pw = os.Getenv(p.PasswordEnv)
		if pw == "" {
			return "", fmt.Errorf("password_env: $%s is not set", p.PasswordEnv)
		}
	case p.PasswordCmd != "":
		out, err := exec.Command("/bin/sh", "-c", p.PasswordCmd).Output()
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) && len(ee.Stderr) > 0 {
				return "", fmt.Errorf("password_cmd failed: %v: %s", err, strings.TrimSpace(string(ee.Stderr)))
			}
			return "", fmt.Errorf("password_cmd failed: %w", err)
		}
		pw = strings.TrimSpace(string(out))
	case p.Password != "":
		pw = p.Password
	default:
		return "", fmt.Errorf("profile %s has no password: set password, password_cmd or password_env", p.Email)
	}
	return strings.ReplaceAll(pw, " ", ""), nil
}

// Addresses returns the account address plus its aliases.
func (p *Profile) Addresses() []string {
	return append([]string{p.Email}, p.Aliases...)
}

// SignatureNames returns the profile's signature names sorted.
func (p *Profile) SignatureNames() []string {
	names := make([]string, 0, len(p.Signatures))
	for n := range p.Signatures {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// LookupSignature finds a signature by name (case-insensitive).
func (p *Profile) LookupSignature(name string) (string, *Signature, bool) {
	if s, ok := p.Signatures[name]; ok {
		return name, s, true
	}
	for n, s := range p.Signatures {
		if strings.EqualFold(n, name) {
			return n, s, true
		}
	}
	return "", nil, false
}

// ResolvedSignature is a signature with any files read in.
type ResolvedSignature struct {
	Name        string
	Description string
	Text        string
	HTML        string
}

// Signature resolves which signature to use. name "" means the profile's
// default (which may be none); "none" means no signature. It returns nil
// when no signature applies.
func (c *Config) Signature(p *Profile, name string) (*ResolvedSignature, error) {
	if name == "" {
		name = p.DefaultSignature
	}
	if name == "" || strings.EqualFold(name, "none") {
		return nil, nil
	}
	canonical, s, ok := p.LookupSignature(name)
	if !ok {
		if len(p.Signatures) == 0 {
			return nil, fmt.Errorf("profile %s has no signatures; add one with `gomail signature set NAME --text ...`", p.Email)
		}
		return nil, fmt.Errorf("unknown signature %q for %s (have: %s)", name, p.Email, strings.Join(p.SignatureNames(), ", "))
	}
	rs := &ResolvedSignature{Name: canonical, Description: s.Description, Text: s.Text, HTML: s.HTML}
	var err error
	if s.TextFile != "" {
		if rs.Text, err = c.readRelative(s.TextFile); err != nil {
			return nil, fmt.Errorf("signature %s: %w", canonical, err)
		}
	}
	if s.HTMLFile != "" {
		if rs.HTML, err = c.readRelative(s.HTMLFile); err != nil {
			return nil, fmt.Errorf("signature %s: %w", canonical, err)
		}
	}
	rs.Text = strings.TrimRight(rs.Text, "\r\n\t ")
	rs.HTML = strings.TrimSpace(rs.HTML)
	if rs.Text == "" && rs.HTML == "" {
		return nil, fmt.Errorf("signature %s is empty", canonical)
	}
	return rs, nil
}

func (c *Config) readRelative(p string) (string, error) {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(c.Dir(), p)
	}
	b, err := os.ReadFile(p)
	return string(b), err
}

// Example is a commented starter config written by `gomail config init`.
const Example = `# gomail configuration
#
# One profile per Gmail / Google Workspace account. Use an app password
# (https://myaccount.google.com/apppasswords, needs 2-Step Verification), never
# your real password. Keep this file private (gomail writes it with mode 0600).

default_profile: personal

profiles:
  personal:
    email: you@gmail.com
    name: Your Name
    # Pick ONE way to supply the app password:
    password: "abcd efgh ijkl mnop"
    # password_env: GOMAIL_PERSONAL_PASSWORD
    # password_cmd: security find-generic-password -s gomail -a you@gmail.com -w
    # aliases: [you@yourdomain.com]      # other "Send mail as" addresses
    default_signature: full
    signatures:
      full:
        description: Name, title and mobile
        text: |
          Your Name
          Founder, Example Co
          +61 400 000 000
        html: |
          <b>Your Name</b><br>Founder, Example Co<br><a href="tel:+61400000000">+61 400 000 000</a>
      no-phone:
        description: For customers - no mobile number
        text: |
          Your Name
          Example Co
      # long HTML signatures can live in files next to this config:
      # fancy:
      #   html_file: signatures/fancy.html

  work:
    email: you@company.com
    name: Your Name
    password_env: GOMAIL_WORK_PASSWORD
    # imap: {host: imap.gmail.com, port: 993}   # defaults
    # smtp: {host: smtp.gmail.com, port: 465}   # defaults (587 uses STARTTLS)
`
