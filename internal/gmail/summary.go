package gmail

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/emersion/go-imap"

	"gomail/internal/model"
)

// Gmail FETCH extensions.
const (
	itemMsgID  imap.FetchItem = "X-GM-MSGID"
	itemThrID  imap.FetchItem = "X-GM-THRID"
	itemLabels imap.FetchItem = "X-GM-LABELS"
)

// summaryItems is everything needed to build a model.MessageSummary.
var summaryItems = []imap.FetchItem{
	imap.FetchEnvelope, imap.FetchFlags, imap.FetchInternalDate, imap.FetchRFC822Size,
	imap.FetchUid, imap.FetchBodyStructure, itemMsgID, itemThrID, itemLabels,
}

// rawSection is BODY.PEEK[]: the whole message, without setting \Seen.
var rawSection = &imap.BodySectionName{Peek: true}

func hasEnvelope(m *imap.Message) bool { return m.Envelope != nil }

// gmNumber reads a 64-bit X-GM-MSGID / X-GM-THRID value. go-imap leaves
// unknown items as raw atoms; imap.ParseNumber would truncate to 32 bits.
func gmNumber(v interface{}) (uint64, error) {
	if n, ok := v.(uint32); ok {
		return uint64(n), nil
	}
	s, err := imap.ParseString(v)
	if err != nil {
		return 0, fmt.Errorf("expected a number, got %T", v)
	}
	return strconv.ParseUint(strings.TrimSpace(s), 10, 64)
}

// gmLabels reads a raw X-GM-LABELS list. Atoms (\Inbox) and quoted strings
// ("\\Important", "My Label") both arrive as strings.
func gmLabels(v interface{}) []string {
	list, ok := v.([]interface{})
	if !ok {
		return nil
	}
	var out []string
	for _, f := range list {
		if s, err := imap.ParseString(f); err == nil && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// systemLabels maps Gmail's backslash labels to their normalized names.
var systemLabels = map[string]string{
	`\inbox`:     "INBOX",
	`\sent`:      "SENT",
	`\draft`:     "DRAFT",
	`\drafts`:    "DRAFT",
	`\important`: "IMPORTANT",
	`\starred`:   "STARRED",
	`\trash`:     "TRASH",
	`\spam`:      "SPAM",
}

// normalizeLabel turns one X-GM-LABELS value into its display form.
func normalizeLabel(raw string) string {
	if strings.HasPrefix(raw, `\`) {
		if l, ok := systemLabels[strings.ToLower(raw)]; ok {
			return l
		}
		return strings.ToUpper(strings.TrimPrefix(raw, `\`))
	}
	if strings.EqualFold(raw, imap.InboxName) {
		return "INBOX"
	}
	return decodeMailboxName(raw)
}

// normalizeLabels normalizes raw X-GM-LABELS values and adds extra
// (already-normalized) labels, returning a sorted, de-duplicated list. Pass
// the selected mailbox's label as extra: Gmail omits it from X-GM-LABELS.
func normalizeLabels(raw []string, extra ...string) []string {
	set := make(map[string]bool, len(raw)+len(extra))
	for _, r := range raw {
		set[normalizeLabel(r)] = true
	}
	for _, e := range extra {
		if e != "" {
			set[e] = true
		}
	}
	out := make([]string, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

func hasFlag(flags []string, flag string) bool {
	for _, f := range flags {
		if strings.EqualFold(f, flag) {
			return true
		}
	}
	return false
}

// newSummary builds a summary from a FETCH of summaryItems in mailbox mb.
func newSummary(m *imap.Message, mb *mailbox) model.MessageSummary {
	s := model.MessageSummary{
		Mailbox: mb.name,
		UID:     m.Uid,
		Size:    m.Size,
		Flags:   m.Flags,
		Unread:  !hasFlag(m.Flags, imap.SeenFlag),
		Starred: hasFlag(m.Flags, imap.FlaggedFlag),
		From:    []model.Address{},
	}
	if v, err := gmNumber(m.Items[itemMsgID]); err == nil {
		s.ID = FormatID(v)
	}
	if v, err := gmNumber(m.Items[itemThrID]); err == nil {
		s.ThreadID = FormatID(v)
	}
	if e := m.Envelope; e != nil {
		s.Date = e.Date
		s.Subject = e.Subject
		s.MessageID = stripAngles(e.MessageId)
		s.InReplyTo = stripAngles(e.InReplyTo)
		if from := addresses(e.From); from != nil {
			s.From = from
		}
		s.To = addresses(e.To)
		s.Cc = addresses(e.Cc)
		s.Bcc = addresses(e.Bcc)
		s.ReplyTo = addresses(e.ReplyTo)
	}
	if s.Date.IsZero() {
		s.Date = m.InternalDate
	}
	if m.BodyStructure != nil {
		s.HasAttach = hasAttachment(m.BodyStructure)
	}
	starred := ""
	if s.Starred {
		starred = "STARRED" // the Starred mailbox is exactly the \Flagged messages
	}
	s.Labels = normalizeLabels(gmLabels(m.Items[itemLabels]), mb.label(), starred)
	return s
}

// addresses converts envelope addresses, skipping RFC 3501 group markers
// (entries without a host). Names were RFC 2047-decoded by go-imap.
func addresses(list []*imap.Address) []model.Address {
	if len(list) == 0 {
		return nil
	}
	out := make([]model.Address, 0, len(list))
	for _, a := range list {
		if a == nil || a.MailboxName == "" || a.HostName == "" {
			continue
		}
		out = append(out, model.Address{Name: strings.TrimSpace(a.PersonalName), Email: a.Address()})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// stripAngles returns the first message id in s without its angle brackets.
func stripAngles(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '<'); i >= 0 {
		if j := strings.IndexByte(s[i:], '>'); j > 0 {
			return s[i+1 : i+j]
		}
		return strings.TrimSpace(s[i+1:])
	}
	return s
}

// bodyBytes returns the content of a fetched body section, or nil.
func bodyBytes(m *imap.Message, section *imap.BodySectionName) []byte {
	lit := m.GetBody(section)
	if lit == nil {
		return nil
	}
	b, err := io.ReadAll(lit)
	if err != nil {
		return nil
	}
	return b
}

// walkLeaves calls fn for each non-multipart part with its IMAP section path
// and the subtype of the enclosing multipart ("" for a single-part message,
// whose body is section 1). fn returns false to stop.
func walkLeaves(bs *imap.BodyStructure, fn func(path []int, part *imap.BodyStructure, parent string) bool) {
	if !isMultipart(bs) {
		fn([]int{1}, bs, "")
		return
	}
	walkParts(bs, nil, fn)
}

func walkParts(bs *imap.BodyStructure, prefix []int, fn func([]int, *imap.BodyStructure, string) bool) bool {
	for i, p := range bs.Parts {
		path := append(append([]int(nil), prefix...), i+1)
		if isMultipart(p) {
			if !walkParts(p, path, fn) {
				return false
			}
		} else if !fn(path, p, strings.ToLower(bs.MIMESubType)) {
			return false
		}
	}
	return true
}

func isMultipart(bs *imap.BodyStructure) bool {
	return len(bs.Parts) > 0 || strings.EqualFold(bs.MIMEType, "multipart")
}

// hasAttachment reports whether any leaf part is an attachment.
func hasAttachment(bs *imap.BodyStructure) bool {
	found := false
	walkLeaves(bs, func(_ []int, p *imap.BodyStructure, parent string) bool {
		found = isAttachment(p, parent)
		return !found
	})
	return found
}

// isAttachment classifies a leaf part. Disposition "attachment" always
// counts, as does an attached message/rfc822. Otherwise a part needs a
// filename, and embedded images (a Content-ID that is inline, or inside
// multipart/related without a disposition) don't count.
func isAttachment(p *imap.BodyStructure, parent string) bool {
	disp := strings.ToLower(p.Disposition)
	switch {
	case disp == "attachment":
		return true
	case strings.EqualFold(p.MIMEType, "message") && strings.EqualFold(p.MIMESubType, "rfc822") && disp != "inline":
		return true
	case !hasFilename(p):
		return false
	case p.Id != "" && (disp == "inline" || (disp == "" && parent == "related")):
		return false
	default:
		return true
	}
}

// hasFilename checks Content-Disposition filename and Content-Type name,
// including RFC 2231 forms (filename*, filename*0*) that go-imap leaves
// undecoded.
func hasFilename(p *imap.BodyStructure) bool {
	for _, params := range []map[string]string{p.DispositionParams, p.Params} {
		for k, v := range params {
			k = strings.ToLower(k)
			if v != "" && (k == "filename" || k == "name" || strings.HasPrefix(k, "filename*") || strings.HasPrefix(k, "name*")) {
				return true
			}
		}
	}
	return false
}

// textPart is the body part used for a snippet.
type textPart struct {
	path      []int
	encoding  string
	mediaType string
	charset   string
}

// snippetPart picks the first non-attachment text/plain leaf, else the first
// text/html one.
func snippetPart(bs *imap.BodyStructure) (textPart, bool) {
	var plain, html *textPart
	walkLeaves(bs, func(path []int, p *imap.BodyStructure, parent string) bool {
		if !strings.EqualFold(p.MIMEType, "text") || isAttachment(p, parent) {
			return true
		}
		tp := &textPart{
			path:      path,
			encoding:  strings.ToLower(p.Encoding),
			mediaType: "text/" + strings.ToLower(p.MIMESubType),
			charset:   p.Params["charset"],
		}
		switch {
		case tp.mediaType == "text/plain" && plain == nil:
			plain = tp
			return false
		case tp.mediaType == "text/html" && html == nil:
			html = tp
		}
		return true
	})
	if plain != nil {
		return *plain, true
	}
	if html != nil {
		return *html, true
	}
	return textPart{}, false
}

func sectionPath(path []int) string {
	parts := make([]string, len(path))
	for i, n := range path {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}
