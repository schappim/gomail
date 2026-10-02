# gomail

A single, self-contained Go binary for working with several Gmail / Google
Workspace accounts from the command line, built for both people and scripts
or agents:

- **Search** with Gmail's own search syntax (`from:`, `has:attachment`, `in:sent`, `label:`, `after:`…)
- **Browse** labels and folders (Inbox, Sent, Drafts, All Mail, Spam, Trash, your labels)
- **Read** messages as text or HTML (opened in a browser with embedded images intact)
- **Download** whole threads (transcript, JSON, original `.eml` files, HTML, attachments) and individual attachments
- **Compose** plain-text, HTML or Markdown mail with attachments and inline images, and **preview** exactly what will go out
- **Send**, **reply** (and reply-all), **forward**, and manage **drafts** that show up in Gmail
- **Profiles**, one per account, using **app passwords**
- **Several named signatures per account**, e.g. `full` with your mobile and `no-phone` for customers
- **Label, star, mark read/unread, archive and trash** messages or whole threads
- `--json` on every command for machine-readable output

It uses IMAP and SMTP with Gmail's IMAP extensions (`X-GM-RAW` search,
`X-GM-MSGID`/`X-GM-THRID` ids, `X-GM-LABELS`). Message and thread ids are
Gmail's own hex ids, the same ones the Gmail web UI uses.

## Install

With Homebrew (macOS or Linux):

```sh
brew tap schappim/gomail
brew install gomail
```

From source (Go 1.27+):

```sh
make install            # static binary in ~/.local/bin/gomail
make dist               # cross-compiled binaries for macOS, Linux and Windows in dist/
```

The binary is self-contained, with no runtime dependencies.

## Set up an account

1. Turn on 2-Step Verification for the Google account.
2. Create an app password at <https://myaccount.google.com/apppasswords>.
3. Make sure IMAP is enabled (Gmail → Settings → Forwarding and POP/IMAP). It
   is on by default for new accounts.
4. Add a profile and test it:

```sh
gomail profile add personal --email you@example.com --name "Alex Example" --default
# prompts for the app password (hidden) and saves it in the config

gomail profile add work --email you@company.example --name "Alex Example" \
  --password-cmd "security find-generic-password -s gomail-work -w"   # macOS Keychain

gomail profile test personal
```

Select a profile per command with `-p NAME` (a profile name or its email
address), or with `GOMAIL_PROFILE`. Without either, `default_profile` is used.

### Passwords

Each profile supplies its app password in one of three ways:

| key | behaviour |
|---|---|
| `password` | stored in the config file (written with mode 0600; gomail warns if the file becomes readable by others) |
| `password_env` | read from an environment variable at run time |
| `password_cmd` | output of a shell command, e.g. a macOS Keychain lookup: `security add-generic-password -s gomail-work -a me@company.com -w` stores it, and `security find-generic-password -s gomail-work -w` reads it |

### Signatures

Every profile can hold any number of named signatures, each with a text
and/or HTML version:

```sh
gomail -p personal signature set full --default \
  --text $'Alex Example\nExample Co\n+61 400 000 000' \
  --html '<b>Alex Example</b><br>Example Co<br>+61 400 000 000'
gomail -p personal signature set no-phone --text $'Alex Example\nExample Co' \
  --description "for customers"
gomail -p personal signature list

# reply to a customer from the personal account without the mobile number
gomail -p personal reply 18c3f2a1b2c3d4e5 -b "Thanks, it's on its way." -S no-phone --draft
```

`--signature NAME` (`-S`) picks a signature, `--no-signature` sends none, and
otherwise the profile's `default_signature` is used. If a signature has only a
text version, an HTML version is generated, and vice versa. Long HTML
signatures can live in files (`html_file: signatures/full.html`, relative to
the config file).

### Config file

`~/.config/gomail/config.yaml`, or `$XDG_CONFIG_HOME/gomail/config.yaml`; override
with `--config` or `GOMAIL_CONFIG`. `gomail config init` writes a commented
example, and `gomail help config` shows it. Commands that change the config
(`profile add`, `signature set`, …) rewrite the file, so hand-written comments
are not preserved.

```yaml
default_profile: personal
profiles:
  personal:
    email: you@example.com
    name: Alex Example
    password_cmd: security find-generic-password -s gomail-personal -w
    aliases: [you@yourdomain.example]     # other send-as addresses
    default_signature: full
    signatures:
      full:
        text: |
          Alex Example
          +61 400 000 000
      no-phone:
        description: for customers
        text: Alex Example
  work:
    email: you@company.example
    password_env: GOMAIL_WORK_PASSWORD
    imap: {host: imap.gmail.com, port: 993}   # defaults; override for other servers
    smtp: {host: smtp.gmail.com, port: 465}   # 587 uses STARTTLS
```

## Usage

```sh
# Labels / folders
gomail labels --counts
gomail list                       # newest in the inbox
gomail list sent -n 10
gomail list "Clients/Acme" --unread

# Search (All Mail by default; Gmail syntax)
gomail search from:acme.com has:attachment after:2026/09/01
gomail search 'subject:"purchase order"' --label inbox --snippets
gomail search invoice --json | jq '.messages[].id'

# Read
gomail read 18c3f2a1b2c3d4e5
gomail read 18c3f2a1b2c3d4e5 --open          # HTML in a browser
gomail read 18c3f2a1b2c3d4e5 --raw > msg.eml

# Threads
gomail thread 18c3f2a1b2c3d4e5               # whole conversation, quotes trimmed
gomail download 18c3f2a1b2c3d4e5 -o ~/cases/acme

# Attachments
gomail attachments 18c3f2a1b2c3d4e5
gomail attachments 18c3f2a1b2c3d4e5 -o ~/Downloads --name '*.pdf'
gomail attachments 18c3f2a1b2c3d4e5 --index 1 --stdout > invoice.pdf

# Compose: preview first, then draft or send
gomail preview --to jane@example.com -s "Quote" -m $'Hi Jane,\n\nQuote **attached**.' -a quote.pdf --open
gomail draft create --to jane@example.com -s "Quote" --markdown-file quote.md -a quote.pdf
gomail send --to jane@example.com -s "Quote" --body-file quote.txt -a quote.pdf      # asks to confirm
gomail send ... --yes                                                                # no prompt (scripts)

# Reply / forward (preview unless --draft or --send)
gomail reply 18c3f2a1b2c3d4e5 --all -m "Sounds good." --draft
gomail forward 18c3f2a1b2c3d4e5 --to accounts@example.com -b "FYI" --send

# Drafts
gomail draft list
gomail draft show 18c3f2a1b2c3d4e5 --open
gomail draft update 18c3f2a1b2c3d4e5 -m "New text" -S no-phone   # returns a NEW id
gomail draft send 18c3f2a1b2c3d4e5
gomail draft delete 18c3f2a1b2c3d4e5

# Organise
gomail modify 18c3f2a1b2c3d4e5 --read --add-label Clients/Acme --archive
gomail modify 18c3f2a1b2c3d4e5 --thread --star
gomail archive 18c3f2a1b2c3d4e5
gomail trash 18c3f2a1b2c3d4e5
gomail label create Clients/Acme
```

### Bodies, HTML and plain text

| flags | result |
|---|---|
| `--body` / `--body-file` | `text/plain` only |
| `--html` / `--html-file` | `multipart/alternative`: your HTML plus a generated plain-text version |
| `--body` + `--html` | both, exactly as given |
| `--markdown` / `--markdown-file` (`-m`) | Markdown rendered to HTML (GitHub-flavoured), with the Markdown source as the text part |
| `--attach` (`-a`) | adds `multipart/mixed` attachments |
| `--inline img.png` | embeds an image for the HTML, referenced as `<img src="cid:img.png">` |

`-` reads a body file from stdin. Replies quote the original the way Gmail
does: the signature goes above the quote, and the HTML part quotes the
original HTML. Threading headers (`In-Reply-To`, `References`, `Re:`) keep the
reply in the same Gmail conversation.

### Sending safety

`send`, `reply --send`, `forward --send` and `draft send` show the message and
ask for confirmation. Without a terminal (scripts, agents) they refuse to send
unless `--yes` is given. `preview` and `--dry-run` never send. Gmail files sent
mail in Sent automatically.

### JSON output

Every command takes `--json`. Errors are printed as `{"error": "..."}` with a
non-zero exit code (1 for runtime errors, 2 for usage errors). HTML bodies and
full header lists are left out of message JSON unless `--html` / `--headers`
is given (`has_html` tells you whether there is one).

## Notes

- Reading never marks messages as read (use `--mark-read` or `modify --read`).
- Label names resolve case-insensitively. The roles `inbox`, `sent`, `drafts`,
  `all`, `trash`, `spam`, `starred` and `important` work on any account
  language or region, because they are found through IMAP SPECIAL-USE flags.
- Gmail caps messages at 25 MB; gomail refuses larger attachment sets.
- `--debug` traces IMAP/SMTP traffic to stderr with credentials redacted.
- `skills/gomail/SKILL.md` is an [Agent Skills](https://agentskills.io) skill
  that teaches any coding agent (Claude Code, Codex, Gemini CLI, Cursor,
  OpenCode, Factory…) to drive gomail safely. `make install-skill` links it
  into `~/.agents/skills/gomail` and from there into each agent's skills folder.

## Layout

```
cmd/gomail          main package
internal/cli        commands (cobra)
internal/config     profiles, passwords, signatures
internal/gmail      IMAP client with Gmail extensions (go-imap v1)
internal/mailparse  MIME parsing: text/HTML bodies, attachments, charsets
internal/htmltext   HTML → readable plain text
internal/compose    message building: bodies, signatures, quoting, attachments
internal/smtpsend   SMTP submission (implicit TLS / STARTTLS)
internal/model      shared types
skills/gomail       Agent Skills skill (SKILL.md) for AI agents
```
