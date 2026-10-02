package gmail

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/commands"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-message/textproto"

	"gomail/internal/model"
)

// FetchRaw returns a message's RFC 822 bytes and summary. It looks in All Mail
// first, then Drafts, Trash and Spam (which All Mail excludes). It never sets
// \Seen.
func (c *Client) FetchRaw(id string) ([]byte, *model.MessageSummary, error) {
	n, err := ParseID(id)
	if err != nil {
		return nil, nil, err
	}
	for _, role := range []string{model.RoleAll, model.RoleDrafts, model.RoleTrash, model.RoleSpam} {
		mb, err := c.byRole(role)
		if err != nil {
			continue // hidden from IMAP; try the next one
		}
		if _, err := c.selectMailbox(mb, false); err != nil {
			return nil, nil, err
		}
		uids, err := c.uidSearch(msgIDCriteria(n)...)
		if err != nil {
			return nil, nil, fmt.Errorf("find message %s in %q: %w", id, mb.name, err)
		}
		if len(uids) == 0 {
			continue
		}
		msgs, err := c.fetchSummaries(mb, seqSetOf(uids[len(uids)-1:]), true, []imap.FetchItem{rawSection.FetchItem()})
		if err != nil {
			return nil, nil, err
		}
		if len(msgs) == 0 {
			continue
		}
		return bodyBytes(msgs[0].msg, rawSection), &msgs[0].summary, nil
	}
	return nil, nil, fmt.Errorf("message %s: %w", id, ErrNotFound)
}

// Thread returns the messages of a thread, oldest first. id may be a thread id
// or the id of any message in the thread.
func (c *Client) Thread(id string, withRaw bool) ([]RawMessage, error) {
	n, err := ParseID(id)
	if err != nil {
		return nil, err
	}
	mb, err := c.byRole(model.RoleAll)
	if err != nil {
		return nil, err
	}
	if _, err := c.selectMailbox(mb, false); err != nil {
		return nil, err
	}
	uids, err := c.threadUIDs(n)
	if err != nil {
		return nil, fmt.Errorf("thread %s: %w", id, err)
	}
	if len(uids) == 0 {
		return nil, fmt.Errorf("thread %s: %w", id, ErrNotFound)
	}

	var extra []imap.FetchItem
	if withRaw {
		extra = []imap.FetchItem{rawSection.FetchItem()}
	}
	msgs, err := c.fetchSummaries(mb, seqSetOf(uids), true, extra)
	if err != nil {
		return nil, err
	}
	sortOldestFirst(msgs)
	out := make([]RawMessage, 0, len(msgs))
	for _, m := range msgs {
		rm := RawMessage{Summary: m.summary}
		if withRaw {
			rm.Raw = bodyBytes(m.msg, rawSection)
		}
		out = append(out, rm)
	}
	return out, nil
}

// threadUIDs finds the UIDs of thread n in the selected mailbox. If n is not
// a thread id it is tried as a message id, whose thread is then used.
func (c *Client) threadUIDs(n uint64) ([]uint32, error) {
	uids, err := c.uidSearch(threadIDCriteria(n)...)
	if err != nil || len(uids) > 0 {
		return uids, err
	}
	msgUIDs, err := c.uidSearch(msgIDCriteria(n)...)
	if err != nil || len(msgUIDs) == 0 {
		return nil, err
	}
	msgs, err := c.fetch(seqSetOf(msgUIDs[:1]), true, []imap.FetchItem{imap.FetchUid, itemThrID}, nil)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, nil
	}
	thread, err := gmNumber(msgs[0].Items[itemThrID])
	if err != nil {
		return nil, fmt.Errorf("read X-GM-THRID: %w", err)
	}
	return c.uidSearch(threadIDCriteria(thread)...)
}

// AppendDraft saves raw as a new draft (flags \Draft \Seen) and returns its
// summary.
func (c *Client) AppendDraft(raw []byte) (*model.MessageSummary, error) {
	mb, err := c.byRole(model.RoleDrafts)
	if err != nil {
		return nil, err
	}
	raw = toCRLF(raw)
	cmd := &commands.Append{
		Mailbox: mb.name,
		Flags:   []string{imap.DraftFlag, imap.SeenFlag},
		Message: bytes.NewBuffer(raw),
	}
	status, err := c.run(cmd, nil)
	if err != nil {
		return nil, fmt.Errorf("save draft to %q: %w", mb.name, err)
	}

	sel, err := c.selectMailbox(mb, false)
	if err != nil {
		return nil, err
	}
	uid, err := c.locateAppended(status, sel, messageIDOf(raw))
	if err != nil {
		return nil, fmt.Errorf("draft saved, but locating it in %q failed: %w", mb.name, err)
	}
	msgs, err := c.fetchSummaries(mb, seqSetOf([]uint32{uid}), true, nil)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("draft saved, but UID %d is not in %q", uid, mb.name)
	}
	return &msgs[0].summary, nil
}

// A freshly appended draft can take a moment to become searchable; retry for
// about three seconds.
const (
	locateRetries = 6
	locateDelay   = 500 * time.Millisecond
)

// locateAppended finds the UID of a just-appended message in the selected
// mailbox: from the APPENDUID response code (UIDPLUS) when it matches the
// mailbox's UIDVALIDITY, else by Message-ID (retrying while Gmail indexes
// it), else the highest UID.
func (c *Client) locateAppended(status *imap.StatusResp, sel *imap.MailboxStatus, messageID string) (uint32, error) {
	if validity, uid, ok := appendUID(status); ok && (sel.UidValidity == 0 || validity == sel.UidValidity) {
		return uid, nil
	}
	for attempt := 0; messageID != ""; attempt++ {
		uids, err := c.searchMessageID(messageID)
		if err != nil {
			return 0, err
		}
		if len(uids) > 0 {
			return uids[len(uids)-1], nil
		}
		if attempt == locateRetries {
			break
		}
		c.sleep(locateDelay)
		if err := c.imap.Noop(); err != nil { // lets Gmail report the new message
			return 0, err
		}
	}
	uids, err := c.uidSearch(imap.RawString("ALL"))
	if err != nil {
		return 0, err
	}
	if len(uids) == 0 {
		return 0, ErrNotFound
	}
	return uids[len(uids)-1], nil
}

// appendUID parses "[APPENDUID <uidvalidity> <uid>]".
func appendUID(status *imap.StatusResp) (validity, uid uint32, ok bool) {
	if status == nil || status.Code != "APPENDUID" || len(status.Arguments) < 2 {
		return 0, 0, false
	}
	validity, err1 := imap.ParseNumber(status.Arguments[0])
	uid, err2 := imap.ParseNumber(status.Arguments[1])
	return validity, uid, err1 == nil && err2 == nil && uid != 0
}

// searchMessageID finds messages by RFC 5322 Message-ID in the selected
// mailbox: Gmail's rfc822msgid: operator first, then a HEADER search.
func (c *Client) searchMessageID(messageID string) ([]uint32, error) {
	uids, err := c.uidSearch(rawSearchCriteria("rfc822msgid:" + messageID)...)
	if err != nil || len(uids) > 0 {
		return uids, err
	}
	return c.uidSearch(imap.RawString("HEADER"), imap.RawString("Message-ID"), messageID)
}

// messageIDOf extracts the Message-ID (without brackets) from raw headers.
func messageIDOf(raw []byte) string {
	h, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		return ""
	}
	mh := mail.Header{Header: message.Header{Header: h}}
	id, err := mh.MessageID()
	if err != nil {
		return stripAngles(mh.Get("Message-Id"))
	}
	return id
}

// toCRLF converts bare LF line endings to CRLF, as IMAP APPEND requires.
func toCRLF(raw []byte) []byte {
	if !bytes.Contains(raw, []byte("\n")) {
		return raw
	}
	out := make([]byte, 0, len(raw)+bytes.Count(raw, []byte("\n")))
	for i, b := range raw {
		if b == '\n' && (i == 0 || raw[i-1] != '\r') {
			out = append(out, '\r')
		}
		out = append(out, b)
	}
	return out
}

// DeleteDraft permanently removes a draft. It fails with ErrNotDraft if the
// message is not in the drafts mailbox.
func (c *Client) DeleteDraft(id string) error {
	n, err := ParseID(id)
	if err != nil {
		return err
	}
	mb, err := c.byRole(model.RoleDrafts)
	if err != nil {
		return err
	}
	if _, err := c.selectMailbox(mb, true); err != nil {
		return err
	}
	uids, err := c.uidSearch(msgIDCriteria(n)...)
	if err != nil {
		return fmt.Errorf("find draft %s: %w", id, err)
	}
	if len(uids) == 0 {
		return fmt.Errorf("message %s: %w", id, ErrNotDraft)
	}
	set := seqSetOf(uids)
	if err := c.storeUIDs(set, "+FLAGS.SILENT", []interface{}{imap.RawString(imap.DeletedFlag)}); err != nil {
		return fmt.Errorf("delete draft %s: %w", id, err)
	}
	if err := c.expungeUIDs(set); err != nil {
		return fmt.Errorf("delete draft %s: expunge: %w", id, err)
	}
	return nil
}

// FindByMessageID finds a message by its RFC 5322 Message-ID (with or without
// angle brackets), searching All Mail and then Drafts. The newest match wins.
func (c *Client) FindByMessageID(messageID string) (*model.MessageSummary, error) {
	mid := stripAngles(messageID)
	if mid == "" {
		return nil, fmt.Errorf("empty Message-ID")
	}
	for _, role := range []string{model.RoleAll, model.RoleDrafts} {
		mb, err := c.byRole(role)
		if err != nil {
			continue
		}
		if _, err := c.selectMailbox(mb, false); err != nil {
			return nil, err
		}
		uids, err := c.searchMessageID(mid)
		if err != nil {
			return nil, fmt.Errorf("find Message-ID <%s> in %q: %w", mid, mb.name, err)
		}
		if len(uids) == 0 {
			continue
		}
		msgs, err := c.fetchSummaries(mb, seqSetOf(uids), true, nil)
		if err != nil {
			return nil, err
		}
		msgs = preferExactMessageID(msgs, mid)
		if len(msgs) == 0 {
			continue
		}
		sortNewestFirst(msgs)
		return &msgs[0].summary, nil
	}
	return nil, fmt.Errorf("message with Message-ID <%s>: %w", mid, ErrNotFound)
}

// preferExactMessageID drops matches whose envelope Message-ID differs
// (HEADER search is a substring match), unless that would drop them all.
func preferExactMessageID(msgs []fetchedMessage, mid string) []fetchedMessage {
	var exact []fetchedMessage
	for _, m := range msgs {
		if m.summary.MessageID == mid {
			exact = append(exact, m)
		}
	}
	if len(exact) == 0 {
		return msgs
	}
	return exact
}

// labelAliases map user-facing names to Gmail system labels for X-GM-LABELS.
var labelAliases = map[string]string{
	"inbox":     `\Inbox`,
	"starred":   `\Starred`,
	"important": `\Important`,
}

// labelValue encodes one label for STORE X-GM-LABELS: a system label as an
// atom, a user label as a quoted modified UTF-7 string.
func labelValue(label string) interface{} {
	if sys, ok := labelAliases[strings.ToLower(label)]; ok {
		return imap.RawString(sys)
	}
	return encodeMailboxName(label)
}

// ModifyLabels adds and removes Gmail labels on messages. inbox, starred and
// important map to the system labels; other names are user labels (Gmail
// creates a missing one on add).
func (c *Client) ModifyLabels(ids []string, add, remove []string) error {
	set, err := c.uidsInAll(ids, true)
	if err != nil {
		return err
	}
	for _, change := range []struct {
		item   string
		labels []string
	}{{"+X-GM-LABELS", add}, {"-X-GM-LABELS", remove}} {
		if len(change.labels) == 0 {
			continue
		}
		values := make([]interface{}, 0, len(change.labels))
		for _, l := range change.labels {
			values = append(values, labelValue(l))
		}
		if err := c.storeUIDs(set, change.item, values); err != nil {
			return fmt.Errorf("update labels: %w", err)
		}
	}
	return nil
}

// MarkRead sets or clears \Seen.
func (c *Client) MarkRead(ids []string, read bool) error {
	return c.setFlag(ids, imap.SeenFlag, read)
}

// Star sets or clears \Flagged (Gmail's star).
func (c *Client) Star(ids []string, starred bool) error {
	return c.setFlag(ids, imap.FlaggedFlag, starred)
}

func (c *Client) setFlag(ids []string, flag string, on bool) error {
	set, err := c.uidsInAll(ids, true)
	if err != nil {
		return err
	}
	item := "-FLAGS.SILENT"
	if on {
		item = "+FLAGS.SILENT"
	}
	if err := c.storeUIDs(set, item, []interface{}{imap.RawString(flag)}); err != nil {
		return fmt.Errorf("set %s: %w", flag, err)
	}
	return nil
}

// Archive removes messages from the inbox (they stay in All Mail).
func (c *Client) Archive(ids []string) error {
	set, err := c.uidsInAll(ids, true)
	if err != nil {
		return err
	}
	if err := c.storeUIDs(set, "-X-GM-LABELS", []interface{}{imap.RawString(`\Inbox`)}); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	return nil
}

// Trash moves messages to the trash mailbox.
func (c *Client) Trash(ids []string) error {
	trash, err := c.byRole(model.RoleTrash)
	if err != nil {
		return err
	}
	set, err := c.uidsInAll(ids, true)
	if err != nil {
		return err
	}
	if c.supports("MOVE") {
		if _, err := c.run(&commands.Uid{Cmd: &commands.Move{SeqSet: set, Mailbox: trash.name}}, nil); err != nil {
			return fmt.Errorf("move to %q: %w", trash.name, err)
		}
		return nil
	}
	if _, err := c.run(&commands.Uid{Cmd: &commands.Copy{SeqSet: set, Mailbox: trash.name}}, nil); err != nil {
		return fmt.Errorf("copy to %q: %w", trash.name, err)
	}
	if err := c.storeUIDs(set, "+FLAGS.SILENT", []interface{}{imap.RawString(imap.DeletedFlag)}); err != nil {
		return fmt.Errorf("trash: %w", err)
	}
	if err := c.expungeUIDs(set); err != nil {
		return fmt.Errorf("trash: expunge: %w", err)
	}
	return nil
}

// maxORTerms bounds each OR-chained X-GM-MSGID search so command lines stay
// short.
const maxORTerms = 50

// uidsInAll opens All Mail and maps Gmail message ids to UIDs. Every id must
// exist; unknown ones are reported together.
func (c *Client) uidsInAll(ids []string, writable bool) (*imap.SeqSet, error) {
	nums, err := parseIDs(ids)
	if err != nil {
		return nil, err
	}
	mb, err := c.byRole(model.RoleAll)
	if err != nil {
		return nil, err
	}
	if _, err := c.selectMailbox(mb, writable); err != nil {
		return nil, err
	}

	set := new(imap.SeqSet)
	found := make(map[uint64]bool)
	for start := 0; start < len(nums); start += maxORTerms {
		chunk := nums[start:min(start+maxORTerms, len(nums))]
		if err := c.resolveChunk(chunk, set, found); err != nil {
			return nil, err
		}
	}
	var missing []string
	for i, n := range nums {
		if !found[n] {
			missing = append(missing, ids[i])
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("message %s in %q: %w", strings.Join(missing, ", "), mb.name, ErrNotFound)
	}
	return set, nil
}

// resolveChunk searches for up to maxORTerms message ids at once. With more
// than one id it fetches X-GM-MSGID for the hits to learn which ids matched.
func (c *Client) resolveChunk(nums []uint64, set *imap.SeqSet, found map[uint64]bool) error {
	uids, err := c.uidSearch(orCriteria(nums)...)
	if err != nil {
		return fmt.Errorf("look up message ids: %w", err)
	}
	if len(uids) == 0 {
		return nil
	}
	set.AddNum(uids...)
	if len(nums) == 1 {
		found[nums[0]] = true
		return nil
	}
	msgs, err := c.fetch(seqSetOf(uids), true, []imap.FetchItem{imap.FetchUid, itemMsgID}, nil)
	if err != nil {
		return fmt.Errorf("look up message ids: %w", err)
	}
	for _, m := range msgs {
		if v, err := gmNumber(m.Items[itemMsgID]); err == nil {
			found[v] = true
		}
	}
	return nil
}

// orCriteria builds "OR X-GM-MSGID a OR X-GM-MSGID b X-GM-MSGID c".
func orCriteria(nums []uint64) []interface{} {
	var out []interface{}
	for i, n := range nums {
		if i < len(nums)-1 {
			out = append(out, imap.RawString("OR"))
		}
		out = append(out, msgIDCriteria(n)...)
	}
	return out
}
