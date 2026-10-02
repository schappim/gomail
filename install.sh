#!/bin/sh
# gomail installer for Linux and macOS (x86_64 and ARM64).
#
#   curl -fsSL https://raw.githubusercontent.com/schappim/gomail/main/install.sh | bash
#
# Downloads the right prebuilt binary for this machine from the GitHub release,
# checks it against the release's SHA256SUMS, and installs it. See --help for
# options.

set -eu

REPO="schappim/gomail"
INSTALL_DIR="${GOMAIL_INSTALL_DIR:-}"
VERSION="${GOMAIL_VERSION:-latest}"
WITH_SKILL="${GOMAIL_SKILL:-0}"

say() { printf '%s\n' "$*"; }
usage() {
	cat <<'USAGE'
Usage: install.sh [--dir DIR] [--version VER] [--skill]

  --dir DIR      install into DIR (default: /usr/local/bin as root, else ~/.local/bin)
  --version VER  install a specific release, e.g. 0.1.0 (default: latest)
  --skill        also install the Agent Skills skill for AI agents into
                 ~/.agents/skills/gomail and link it for agents that are set up

Options can also come from GOMAIL_INSTALL_DIR, GOMAIL_VERSION and GOMAIL_SKILL=1.
When piping, pass options after "bash -s --":
  curl -fsSL https://raw.githubusercontent.com/schappim/gomail/main/install.sh | bash -s -- --skill
USAGE
}
fail() { printf 'gomail install: %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
	case "$1" in
	--dir) [ $# -ge 2 ] || fail "--dir needs a directory"; INSTALL_DIR="$2"; shift 2 ;;
	--dir=*) INSTALL_DIR="${1#--dir=}"; shift ;;
	--version) [ $# -ge 2 ] || fail "--version needs a value"; VERSION="$2"; shift 2 ;;
	--version=*) VERSION="${1#--version=}"; shift ;;
	--skill) WITH_SKILL=1; shift ;;
	-h | --help) usage; exit 0 ;;
	*) fail "unknown option: $1" ;;
	esac
done

# Work out the release asset for this OS and CPU.
os=$(uname -s)
case "$os" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) fail "unsupported OS: $os (prebuilt binaries cover Linux and macOS; on Windows download the .exe from https://github.com/$REPO/releases)" ;;
esac

arch=$(uname -m)
case "$arch" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) fail "unsupported CPU architecture: $arch (prebuilt binaries cover x86_64 and ARM64; other platforms can build from source with Go: https://github.com/$REPO)" ;;
esac
asset="gomail-$os-$arch"

case "$VERSION" in
latest) base="https://github.com/$REPO/releases/latest/download" ;;
v*) base="https://github.com/$REPO/releases/download/$VERSION" ;;
*) base="https://github.com/$REPO/releases/download/v$VERSION" ;;
esac

if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL --retry 3 -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -q -O "$2" "$1"; }
else
	fail "curl or wget is required"
fi

tmp=$(mktemp -d 2>/dev/null || mktemp -d -t gomail)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading $asset ($VERSION)..."
fetch "$base/$asset" "$tmp/gomail" || fail "download failed: $base/$asset"
fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS" || fail "download failed: $base/SHA256SUMS"

expected=$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")
[ -n "$expected" ] || fail "$asset is not listed in SHA256SUMS"
if command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "$tmp/gomail" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
	actual=$(shasum -a 256 "$tmp/gomail" | awk '{print $1}')
else
	fail "sha256sum or shasum is required to verify the download"
fi
[ "$actual" = "$expected" ] || fail "checksum mismatch for $asset (expected $expected, got $actual)"
chmod 755 "$tmp/gomail"

if [ -z "$INSTALL_DIR" ]; then
	if [ "$(id -u)" = 0 ]; then
		INSTALL_DIR=/usr/local/bin
	else
		INSTALL_DIR="$HOME/.local/bin"
	fi
fi
mkdir -p "$INSTALL_DIR" || fail "cannot create $INSTALL_DIR"
[ -w "$INSTALL_DIR" ] || fail "$INSTALL_DIR is not writable (choose another with --dir, or run with sudo)"
mv -f "$tmp/gomail" "$INSTALL_DIR/gomail"
say "Installed $("$INSTALL_DIR/gomail" --version 2>/dev/null || echo gomail) to $INSTALL_DIR/gomail"

case ":$PATH:" in
*":$INSTALL_DIR:"*) ;;
*) say "Note: $INSTALL_DIR is not on your PATH. Add this to your shell profile:"
   say "  export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac

if [ "$WITH_SKILL" = 1 ]; then
	ref=main
	case "$VERSION" in latest) ;; v*) ref="$VERSION" ;; *) ref="v$VERSION" ;; esac
	skill_dir="$HOME/.agents/skills/gomail"
	mkdir -p "$skill_dir"
	fetch "https://raw.githubusercontent.com/$REPO/$ref/skills/gomail/SKILL.md" "$skill_dir/SKILL.md" ||
		fail "could not download the skill"
	say "Installed the gomail skill to $skill_dir"
	for agent_dir in "$HOME/.claude/skills" "$HOME/.codex/skills" "$HOME/.gemini/skills" \
		"$HOME/.cursor/skills" "$HOME/.config/opencode/skills" "$HOME/.factory/skills"; do
		if [ -d "$agent_dir" ] && [ ! -e "$agent_dir/gomail" ]; then
			ln -s "$skill_dir" "$agent_dir/gomail" && say "  linked $agent_dir/gomail"
		fi
	done
fi

say ""
say "Next: create a Google app password (https://myaccount.google.com/apppasswords), then"
say "  gomail profile add personal --email you@gmail.com --name \"Your Name\""
say "  gomail profile test"
