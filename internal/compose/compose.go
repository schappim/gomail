// Package compose builds outgoing RFC 5322 messages: plain text, HTML or
// Markdown bodies, per-account signatures, attachments, and reply/forward
// quoting with correct threading headers.
package compose

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	"gomail/internal/model"
)

// Signature is a named signature with text and/or HTML variants.
type Signature struct {
	Name string
	Text string
	HTML string
}

// File is an attachment (or inline image) to include.
type File struct {
	Filename    string
	ContentType string // detected from the extension / content when empty
	Data        []byte
	ContentID   string // without <>; set for inline images referenced as cid:
	Inline      bool
}

// Options describes the message to build.
type Options struct {
	From    model.Address
	To      []model.Address
	Cc      []model.Address
	Bcc     []model.Address
	ReplyTo []model.Address
	Subject string

	Text     string // plain-text body
	HTML     string // HTML body (fragment or full document)
	Markdown string // Markdown body: rendered to HTML, source used as the text part

	Signature   *Signature
	Attachments []File

	InReplyTo  string         // parent Message-ID, no <>
	References []string       // Message-IDs, no <>
	Quote      *model.Message // original to quote (reply) or include (forward)
	Forward    bool           // quote as a forward rather than a reply

	Date       time.Time      // zero means now
	MessageID  string         // generated when empty, no <>
	IncludeBcc bool           // write the Bcc header (drafts only)
	Headers    []model.Header // extra headers
}

// Built is a composed message plus everything needed to preview or send it.
type Built struct {
	Raw         []byte
	MessageID   string
	Text        string // final text/plain part ("" when absent)
	HTML        string // final text/html part ("" when text-only)
	Recipients  []string
	Attachments []model.Attachment
	Structure   string
}

// MaxAttachmentBytes is Gmail's limit on the total size of attachments.
const MaxAttachmentBytes = 25 << 20

// now is the clock used for the Date header; tests replace it.
var now = time.Now

// Build composes the message.
func Build(o Options) (*Built, error) {
	from, err := cleanAddress(o.From)
	if err != nil {
		return nil, fmt.Errorf("compose: From: %w", err)
	}
	if from.Email == "" {
		return nil, errors.New("compose: a From address is required")
	}
	lists := map[string][]model.Address{}
	for _, l := range []struct {
		name  string
		addrs []model.Address
	}{{"To", o.To}, {"Cc", o.Cc}, {"Bcc", o.Bcc}, {"Reply-To", o.ReplyTo}} {
		cleaned, err := cleanAddresses(l.addrs)
		if err != nil {
			return nil, fmt.Errorf("compose: %s: %w", l.name, err)
		}
		lists[l.name] = cleaned
	}

	var total int
	for _, f := range o.Attachments {
		total += len(f.Data)
	}
	if total > MaxAttachmentBytes {
		return nil, fmt.Errorf("compose: attachments total %s, over Gmail's 25 MB limit; share large files as a Google Drive link instead", humanSize(total))
	}

	body, err := composeBody(&o)
	if err != nil {
		return nil, err
	}

	root, atts, err := buildTree(body, o.Attachments)
	if err != nil {
		return nil, err
	}

	msgID := cleanMsgID(o.MessageID)
	if msgID == "" {
		if msgID, err = generateMessageID(from.Email); err != nil {
			return nil, err
		}
	}
	date := o.Date
	if date.IsZero() {
		date = now()
	}

	fields := []field{{"From", formatAddressList([]model.Address{from})}}
	fields = appendAddressField(fields, "To", lists["To"])
	fields = appendAddressField(fields, "Cc", lists["Cc"])
	if o.IncludeBcc {
		fields = appendAddressField(fields, "Bcc", lists["Bcc"])
	}
	fields = appendAddressField(fields, "Reply-To", lists["Reply-To"])
	fields = append(fields,
		field{"Subject", encodeText(o.Subject)},
		field{"Date", date.Format(dateLayout)},
		field{"Message-ID", "<" + msgID + ">"},
	)
	if id := cleanMsgID(o.InReplyTo); id != "" {
		fields = append(fields, field{"In-Reply-To", "<" + id + ">"})
	}
	if refs := cleanMsgIDs(o.References); len(refs) > 0 {
		fields = append(fields, field{"References", "<" + strings.Join(refs, "> <") + ">"})
	}
	fields = append(fields, field{"User-Agent", "gomail"})
	fields, err = mergeExtraHeaders(fields, o.Headers)
	if err != nil {
		return nil, err
	}
	fields = append(fields, field{"MIME-Version", "1.0"})

	var buf bytes.Buffer
	if err := writeMessage(&buf, fields, root); err != nil {
		return nil, fmt.Errorf("compose: writing message: %w", err)
	}

	var rcpts []string
	for _, name := range []string{"To", "Cc", "Bcc"} {
		for _, a := range lists[name] {
			rcpts = append(rcpts, a.Email)
		}
	}

	return &Built{
		Raw:         buf.Bytes(),
		MessageID:   msgID,
		Text:        body.text,
		HTML:        body.html,
		Recipients:  dedupeFold(rcpts),
		Attachments: atts,
		Structure:   root.structure(),
	}, nil
}

// dateLayout is the RFC 5322 date format used for the Date header.
const dateLayout = "Mon, 2 Jan 2006 15:04:05 -0700"

func humanSize(n int) string {
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

// dedupeFold removes case-insensitive duplicates, keeping the first spelling.
func dedupeFold(in []string) []string {
	seen := make(map[string]bool, len(in))
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		k := strings.ToLower(s)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}
