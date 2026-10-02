package compose

import (
	"strings"

	"gomail/internal/model"
)

// Reply returns Options pre-filled for replying to orig: recipients, subject,
// threading headers and the quote. self lists the user's own addresses; all
// selects reply-all. The body fields are left empty.
func Reply(orig *model.Message, self []string, all bool) Options {
	if orig == nil {
		return Options{}
	}
	isSelf := func(a model.Address) bool {
		for _, s := range self {
			if strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(a.Email)) {
				return true
			}
		}
		return false
	}

	var to []model.Address
	fromSelf := len(orig.From) > 0 && isSelf(orig.From[0])
	switch {
	case fromSelf:
		// Replying to your own sent message goes to its recipients, as in
		// Gmail.
		to = without(orig.To, isSelf)
		if len(to) == 0 {
			to = orig.To
		}
	case len(orig.ReplyTo) > 0:
		to = orig.ReplyTo
	default:
		to = orig.From
	}
	if len(to) == 0 {
		to = orig.From
	}
	to = dedupeAddresses(to, nil)

	var cc []model.Address
	if all {
		inTo := func(a model.Address) bool {
			for _, t := range to {
				if strings.EqualFold(t.Email, a.Email) {
					return true
				}
			}
			return false
		}
		candidates := append(append([]model.Address{}, orig.To...), orig.Cc...)
		cc = dedupeAddresses(candidates, func(a model.Address) bool { return isSelf(a) || inTo(a) })
	}

	refs := append([]string{}, orig.References...)
	if len(refs) == 0 && orig.InReplyTo != "" {
		// RFC 5322 §3.6.4: without References, the parent's In-Reply-To
		// stands in for them.
		refs = append(refs, orig.InReplyTo)
	}
	if orig.MessageID != "" {
		refs = append(refs, orig.MessageID)
	}

	return Options{
		To:         to,
		Cc:         cc,
		Subject:    prefixSubject("Re:", orig.Subject, "re:"),
		InReplyTo:  cleanMsgID(orig.MessageID),
		References: cleanMsgIDs(refs),
		Quote:      orig,
	}
}

// Forward returns Options pre-filled for forwarding orig. With
// includeAttachments the original's attachments are attached again; inline
// images (those with a Content-ID) are always carried over so cid: references
// in the forwarded HTML keep working.
func Forward(orig *model.Message, includeAttachments bool) Options {
	if orig == nil {
		return Options{}
	}
	var files []File
	for _, a := range orig.Attachments {
		inlineImage := a.Inline && a.ContentID != "" && len(a.Data) > 0
		if !includeAttachments && !inlineImage {
			continue
		}
		files = append(files, File{
			Filename:    a.Filename,
			ContentType: a.ContentType,
			Data:        a.Data,
			ContentID:   a.ContentID,
			Inline:      a.Inline,
		})
	}
	return Options{
		Subject:     prefixSubject("Fwd:", orig.Subject, "fwd:", "fw:"),
		Quote:       orig,
		Forward:     true,
		Attachments: files,
	}
}

// prefixSubject adds prefix unless the subject already starts with one of
// the existing prefixes (case-insensitive).
func prefixSubject(prefix, subject string, existing ...string) string {
	s := strings.TrimSpace(subject)
	for _, p := range existing {
		if len(s) >= len(p) && strings.EqualFold(s[:len(p)], p) {
			return s
		}
	}
	if s == "" {
		return prefix
	}
	return prefix + " " + s
}

func without(list []model.Address, drop func(model.Address) bool) []model.Address {
	var out []model.Address
	for _, a := range list {
		if !drop(a) {
			out = append(out, a)
		}
	}
	return out
}

// dedupeAddresses removes case-insensitive duplicate emails, empty
// addresses, and any address drop reports.
func dedupeAddresses(list []model.Address, drop func(model.Address) bool) []model.Address {
	seen := map[string]bool{}
	var out []model.Address
	for _, a := range list {
		k := strings.ToLower(strings.TrimSpace(a.Email))
		if k == "" || seen[k] || (drop != nil && drop(a)) {
			continue
		}
		seen[k] = true
		out = append(out, a)
	}
	return out
}
