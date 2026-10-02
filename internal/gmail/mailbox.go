package gmail

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/commands"
	"github.com/emersion/go-imap/responses"
	"github.com/emersion/go-imap/utf7"

	"gomail/internal/model"
)

// mailbox is one LIST entry.
type mailbox struct {
	name       string // UTF-8 (modified UTF-7 decoded)
	attrs      []string
	role       string // model.Role* or ""
	selectable bool
}

// roleAttrs maps SPECIAL-USE attributes (RFC 6154, plus Gmail's \Important
// and the legacy XLIST names) to roles. Gmail localizes and renames system
// mailboxes ("[Google Mail]/Gesendet"), so attributes are the only reliable
// way to find them.
var roleAttrs = map[string]string{
	`\all`:       model.RoleAll,
	`\allmail`:   model.RoleAll,
	`\drafts`:    model.RoleDrafts,
	`\sent`:      model.RoleSent,
	`\junk`:      model.RoleSpam,
	`\spam`:      model.RoleSpam,
	`\trash`:     model.RoleTrash,
	`\flagged`:   model.RoleStarred,
	`\starred`:   model.RoleStarred,
	`\important`: model.RoleImportant,
}

// roleAliases are the names users can type instead of a mailbox name.
var roleAliases = map[string]string{
	"inbox":     model.RoleInbox,
	"sent":      model.RoleSent,
	"sent-mail": model.RoleSent,
	"drafts":    model.RoleDrafts,
	"draft":     model.RoleDrafts,
	"all":       model.RoleAll,
	"all-mail":  model.RoleAll,
	"archive":   model.RoleAll,
	"trash":     model.RoleTrash,
	"bin":       model.RoleTrash,
	"spam":      model.RoleSpam,
	"junk":      model.RoleSpam,
	"starred":   model.RoleStarred,
	"flagged":   model.RoleStarred,
	"important": model.RoleImportant,
}

// roleLabels are the normalized X-GM-LABELS values carried by every message
// in a system mailbox.
var roleLabels = map[string]string{
	model.RoleInbox:     "INBOX",
	model.RoleSent:      "SENT",
	model.RoleDrafts:    "DRAFT",
	model.RoleStarred:   "STARRED",
	model.RoleImportant: "IMPORTANT",
	model.RoleTrash:     "TRASH",
	model.RoleSpam:      "SPAM",
}

func newMailbox(name string, attrs []string) *mailbox {
	mb := &mailbox{name: imap.CanonicalMailboxName(name), attrs: attrs, selectable: true}
	if mb.name == imap.InboxName {
		mb.role = model.RoleInbox
	}
	for _, a := range attrs {
		a = strings.ToLower(a)
		if role, ok := roleAttrs[a]; ok && mb.role == "" {
			mb.role = role
		}
		if a == `\noselect` || a == `\nonexistent` {
			mb.selectable = false
		}
	}
	return mb
}

// label is the normalized label Gmail leaves out of X-GM-LABELS for messages
// fetched from this mailbox: the system label for system mailboxes, the
// mailbox name for user labels, and nothing for All Mail.
func (mb *mailbox) label() string {
	switch {
	case mb.role == model.RoleAll || !mb.selectable:
		return ""
	case mb.role != "":
		return roleLabels[mb.role]
	default:
		return mb.name
	}
}

func decodeMailboxName(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	if dec, err := utf7.Encoding.NewDecoder().String(s); err == nil {
		return dec
	}
	return s
}

func encodeMailboxName(s string) string {
	if enc, err := utf7.Encoding.NewEncoder().String(s); err == nil {
		return enc
	}
	return s
}

// listHandler collects LIST responses and, for LIST-STATUS, the STATUS
// responses that come with them (keyed by decoded mailbox name).
type listHandler struct {
	mailboxes []*mailbox
	status    map[string]*imap.MailboxStatus
}

func (h *listHandler) Handle(resp imap.Resp) error {
	name, fields, ok := imap.ParseNamedResp(resp)
	if !ok {
		return responses.ErrUnhandled
	}
	switch name {
	case "LIST":
		if mb := parseList(fields); mb != nil {
			h.mailboxes = append(h.mailboxes, mb)
		}
		return nil
	case "STATUS":
		if len(fields) < 2 {
			return nil
		}
		raw, err := imap.ParseString(fields[0])
		items, ok := fields[1].([]interface{})
		if err != nil || !ok {
			return nil
		}
		st := &imap.MailboxStatus{}
		if st.Parse(items) == nil {
			if h.status == nil {
				h.status = make(map[string]*imap.MailboxStatus)
			}
			h.status[imap.CanonicalMailboxName(decodeMailboxName(raw))] = st
		}
		return nil
	}
	return responses.ErrUnhandled
}

// parseList parses "(attrs) delim name". It is lenient: a name that isn't
// valid modified UTF-7 is kept as sent rather than failing the whole LIST.
func parseList(fields []interface{}) *mailbox {
	if len(fields) < 3 {
		return nil
	}
	attrs, err := imap.ParseStringList(fields[0])
	if err != nil {
		return nil
	}
	raw, err := imap.ParseString(fields[2])
	if err != nil {
		return nil
	}
	return newMailbox(decodeMailboxName(raw), attrs)
}

// listMailboxes runs LIST "" "*", optionally with RETURN (STATUS (MESSAGES
// UNSEEN)) when the server has LIST-STATUS (RFC 5819), and refreshes the cache.
// An extended LIST only carries special-use attributes when asked for them
// (RFC 6154), and Gmail omits them otherwise, so SPECIAL-USE is requested too.
func (c *Client) listMailboxes(withStatus bool) (map[string]*imap.MailboxStatus, error) {
	args := []interface{}{"", "*"}
	if withStatus {
		var ret []interface{}
		if c.supports("SPECIAL-USE") {
			ret = append(ret, imap.RawString("SPECIAL-USE"))
		}
		ret = append(ret, imap.RawString("STATUS"), []interface{}{imap.RawString("MESSAGES"), imap.RawString("UNSEEN")})
		args = append(args, imap.RawString("RETURN"), ret)
	}
	h := &listHandler{}
	if _, err := c.run(&imap.Command{Name: "LIST", Arguments: args}, h); err != nil {
		return nil, fmt.Errorf("list mailboxes: %w", err)
	}
	c.mailboxes = h.mailboxes
	return h.status, nil
}

func (c *Client) mailboxList() ([]*mailbox, error) {
	if c.mailboxes == nil {
		if _, err := c.listMailboxes(false); err != nil {
			return nil, err
		}
	}
	return c.mailboxes, nil
}

// Labels lists every mailbox. With counts, it adds MESSAGES and UNSEEN for
// each selectable one (one LIST-STATUS round trip when supported, otherwise a
// STATUS per mailbox).
func (c *Client) Labels(counts bool) ([]model.Label, error) {
	var status map[string]*imap.MailboxStatus
	var err error
	if counts && c.supports("LIST-STATUS") {
		status, err = c.listMailboxes(true)
		var se *serverError
		if errors.As(err, &se) {
			status, err = nil, nil
			c.mailboxes = nil
		}
		if err != nil {
			return nil, err
		}
	}
	mailboxes, err := c.mailboxList()
	if err != nil {
		return nil, err
	}

	labels := make([]model.Label, 0, len(mailboxes))
	for _, mb := range mailboxes {
		l := model.Label{Name: mb.name, Role: mb.role, Attributes: mb.attrs, Selectable: mb.selectable}
		if counts && mb.selectable {
			st := status[mb.name]
			if st == nil {
				if st, err = c.status(mb.name); err != nil {
					return nil, err
				}
			}
			if st != nil {
				messages, unseen := st.Messages, st.Unseen
				l.Messages, l.Unread = &messages, &unseen
			}
		}
		labels = append(labels, l)
	}
	return labels, nil
}

// status runs STATUS (MESSAGES UNSEEN). A NO reply yields nil, not an error,
// so one odd mailbox doesn't break a label listing.
func (c *Client) status(name string) (*imap.MailboxStatus, error) {
	cmd := &commands.Status{Mailbox: name, Items: []imap.StatusItem{imap.StatusMessages, imap.StatusUnseen}}
	res := &responses.Status{Mailbox: new(imap.MailboxStatus)}
	_, err := c.run(cmd, res)
	var se *serverError
	if errors.As(err, &se) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("status %q: %w", name, err)
	}
	return res.Mailbox, nil
}

// ResolveMailbox maps a mailbox name or alias (inbox, sent, drafts, all,
// trash, spam, starred, important and their synonyms) to the account's actual
// mailbox name.
func (c *Client) ResolveMailbox(nameOrAlias string) (string, error) {
	mb, err := c.resolve(nameOrAlias)
	if err != nil {
		return "", err
	}
	return mb.name, nil
}

// resolve prefers, in order: an exact (case-sensitive) mailbox name, so a
// user label called "Archive" stays reachable; a role alias; a
// case-insensitive name match.
func (c *Client) resolve(nameOrAlias string) (*mailbox, error) {
	mailboxes, err := c.mailboxList()
	if err != nil {
		return nil, err
	}
	want := strings.TrimSpace(nameOrAlias)
	for _, mb := range mailboxes {
		if mb.name == want {
			return mb, nil
		}
	}
	if role, ok := roleAliases[strings.ToLower(want)]; ok {
		return c.byRole(role)
	}
	for _, mb := range mailboxes {
		if strings.EqualFold(mb.name, want) {
			return mb, nil
		}
	}
	return nil, notFoundMailbox(want, mailboxes)
}

func notFoundMailbox(want string, mailboxes []*mailbox) error {
	var similar []string
	lw := strings.ToLower(want)
	for _, mb := range mailboxes {
		ln := strings.ToLower(mb.name)
		if mb.selectable && lw != "" && (strings.Contains(ln, lw) || strings.Contains(lw, ln)) {
			similar = append(similar, fmt.Sprintf("%q", mb.name))
		}
	}
	sort.Strings(similar)
	if len(similar) > 5 {
		similar = similar[:5]
	}
	if len(similar) == 0 {
		return fmt.Errorf("mailbox %q not found (list labels to see the available names)", want)
	}
	return fmt.Errorf("mailbox %q not found; did you mean %s?", want, strings.Join(similar, ", "))
}

// byRole finds the mailbox for a role. Gmail lets users hide system labels
// from IMAP, so a missing one gets an explanatory error.
func (c *Client) byRole(role string) (*mailbox, error) {
	mailboxes, err := c.mailboxList()
	if err != nil {
		return nil, err
	}
	for _, mb := range mailboxes {
		if mb.role == role {
			return mb, nil
		}
	}
	return nil, fmt.Errorf("no %s mailbox is visible over IMAP; enable \"Show in IMAP\" for it in Gmail settings (Labels tab)", role)
}

// selectMailbox opens mb, read-only (EXAMINE) unless writable. A mailbox that
// is already open with sufficient access is reused.
func (c *Client) selectMailbox(mb *mailbox, writable bool) (*imap.MailboxStatus, error) {
	if !mb.selectable {
		return nil, fmt.Errorf("mailbox %q cannot be opened (it only groups other labels)", mb.name)
	}
	if cur := c.imap.Mailbox(); cur != nil && c.imap.State() == imap.SelectedState &&
		cur.Name == mb.name && (!writable || !cur.ReadOnly) {
		return cur, nil
	}
	st, err := c.imap.Select(mb.name, !writable)
	if err != nil {
		verb := "examine"
		if writable {
			verb = "select"
		}
		return nil, fmt.Errorf("%s %q: %w", verb, mb.name, err)
	}
	return st, nil
}

// CreateLabel creates a Gmail label (an IMAP mailbox). Nested labels use "/".
func (c *Client) CreateLabel(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("create label: empty name")
	}
	if _, err := c.run(&commands.Create{Mailbox: name}, nil); err != nil {
		return fmt.Errorf("create label %q: %w", name, err)
	}
	c.mailboxes = nil
	return nil
}
