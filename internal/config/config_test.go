package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExampleParses(t *testing.T) {
	c, err := Load(writeConfig(t, Example))
	if err != nil {
		t.Fatal(err)
	}
	name, p, err := c.Resolve("")
	if err != nil || name != "personal" || p.Email != "you@gmail.com" {
		t.Fatalf("Resolve default = %q %+v %v", name, p, err)
	}
	pw, err := p.ResolvePassword()
	if err != nil || pw != "abcdefghijklmnop" {
		t.Fatalf("password = %q, %v (spaces should be stripped)", pw, err)
	}
	rs, err := c.Signature(p, "")
	if err != nil || rs == nil || rs.Name != "full" || !strings.Contains(rs.Text, "+61") {
		t.Fatalf("default signature = %+v, %v", rs, err)
	}
	rs, err = c.Signature(p, "NO-PHONE")
	if err != nil || rs.Name != "no-phone" || strings.Contains(rs.Text, "+61") || rs.HTML != "" {
		t.Fatalf("no-phone signature = %+v, %v", rs, err)
	}
	if rs, err := c.Signature(p, "none"); err != nil || rs != nil {
		t.Fatalf("none = %+v, %v", rs, err)
	}
	if _, err := c.Signature(p, "missing"); err == nil || !strings.Contains(err.Error(), "full, no-phone") {
		t.Fatalf("missing signature error = %v", err)
	}
}

func TestResolveRules(t *testing.T) {
	path := writeConfig(t, `
profiles:
  a: {email: a@example.com}
  b: {email: b@example.com}
`)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOMAIL_PROFILE", "")
	if _, _, err := c.Resolve(""); err == nil || !strings.Contains(err.Error(), "several profiles") {
		t.Fatalf("ambiguous resolve error = %v", err)
	}
	if n, _, err := c.Resolve("B@example.com"); err != nil || n != "b" {
		t.Fatalf("resolve by email = %q, %v", n, err)
	}
	t.Setenv("GOMAIL_PROFILE", "a")
	if n, _, err := c.Resolve(""); err != nil || n != "a" {
		t.Fatalf("resolve via env = %q, %v", n, err)
	}
	if _, _, err := c.Resolve("zzz"); err == nil {
		t.Fatal("expected unknown profile error")
	}
}

func TestPasswordSources(t *testing.T) {
	t.Setenv("GOMAIL_TEST_PW", "wxyz wxyz wxyz wxyz")
	p := &Profile{Email: "x@example.com", PasswordEnv: "GOMAIL_TEST_PW", Password: "ignored"}
	if pw, err := p.ResolvePassword(); err != nil || pw != "wxyzwxyzwxyzwxyz" {
		t.Fatalf("env password = %q, %v", pw, err)
	}
	p = &Profile{Email: "x@example.com", PasswordCmd: "echo 'from cmd'"}
	if pw, err := p.ResolvePassword(); err != nil || pw != "fromcmd" {
		t.Fatalf("cmd password = %q, %v", pw, err)
	}
	p = &Profile{Email: "x@example.com", PasswordCmd: "echo nope >&2; exit 3"}
	if _, err := p.ResolvePassword(); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("failing cmd error = %v", err)
	}
	p = &Profile{Email: "x@example.com"}
	if _, err := p.ResolvePassword(); err == nil {
		t.Fatal("expected missing password error")
	}
}

func TestSignatureFilesAndSave(t *testing.T) {
	path := writeConfig(t, `
default_profile: me
profiles:
  me:
    email: me@example.com
    default_signature: fancy
    signatures:
      fancy:
        html_file: sigs/fancy.html
`)
	dir := filepath.Dir(path)
	os.MkdirAll(filepath.Join(dir, "sigs"), 0o700)
	os.WriteFile(filepath.Join(dir, "sigs", "fancy.html"), []byte("<b>Me</b>\n"), 0o600)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p := c.Profiles["me"]
	rs, err := c.Signature(p, "")
	if err != nil || rs.HTML != "<b>Me</b>" {
		t.Fatalf("file signature = %+v, %v", rs, err)
	}

	p.Signatures["plain"] = &Signature{Text: "Me"}
	p.Password = "secret"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("saved mode = %v", info.Mode().Perm())
	}
	c2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := c2.Profiles["me"].SignatureNames(); strings.Join(got, ",") != "fancy,plain" {
		t.Fatalf("signatures after save = %v", got)
	}
	if c2.InsecurePermissions() {
		t.Fatal("0600 file reported insecure")
	}
	os.Chmod(path, 0o644)
	if !c2.InsecurePermissions() {
		t.Fatal("0644 file with password not reported insecure")
	}
}

func TestMissingFileIsEmpty(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil || len(c.Profiles) != 0 {
		t.Fatalf("Load missing = %+v, %v", c, err)
	}
	if _, _, err := c.Resolve(""); err == nil || !strings.Contains(err.Error(), "profile add") {
		t.Fatalf("empty resolve error = %v", err)
	}
}
