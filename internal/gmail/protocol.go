package gmail

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/commands"
	"github.com/emersion/go-imap/responses"
)

// serverError is a NO or BAD reply to a command.
type serverError struct {
	status *imap.StatusResp
}

func (e *serverError) Error() string { return statusText(e.status) }

func statusText(s *imap.StatusResp) string {
	text := s.Info
	if s.Code != "" {
		text = "[" + string(s.Code) + "] " + text
	}
	if s.Type != imap.StatusRespNo {
		text = string(s.Type) + " " + text
	}
	return strings.TrimSpace(text)
}

// run executes a command. Network failures and NO/BAD replies both come back
// as errors; a NO/BAD is a *serverError.
func (c *Client) run(cmd imap.Commander, h responses.Handler) (*imap.StatusResp, error) {
	status, err := c.imap.Execute(cmd, h)
	if err != nil {
		return nil, err
	}
	if status.Type != imap.StatusRespOk {
		return status, &serverError{status}
	}
	return status, nil
}

func (c *Client) supports(capability string) bool {
	ok, _ := c.imap.Support(capability)
	return ok
}

// uidCommand builds "UID <name> <args...>".
func uidCommand(name string, args ...interface{}) imap.Commander {
	return &commands.Uid{Cmd: &imap.Command{Name: name, Arguments: args}}
}

// uidSearch runs UID SEARCH with raw criteria and returns the matching UIDs in
// ascending order.
func (c *Client) uidSearch(criteria ...interface{}) ([]uint32, error) {
	h := &searchHandler{}
	if _, err := c.run(uidCommand("SEARCH", criteria...), h); err != nil {
		return nil, err
	}
	return sortedUIDs(h.ids), nil
}

func msgIDCriteria(n uint64) []interface{} {
	return []interface{}{imap.RawString("X-GM-MSGID"), imap.RawString(strconv.FormatUint(n, 10))}
}

func threadIDCriteria(n uint64) []interface{} {
	return []interface{}{imap.RawString("X-GM-THRID"), imap.RawString(strconv.FormatUint(n, 10))}
}

// rawSearchCriteria builds the X-GM-RAW criteria for a Gmail query. Printable
// ASCII goes out as a quoted string; anything else needs CHARSET UTF-8 and a
// literal, because IMAP quoted strings are 7-bit.
func rawSearchCriteria(query string) []interface{} {
	query = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(query)
	if isPrintableASCII(query) {
		return []interface{}{imap.RawString("X-GM-RAW"), query}
	}
	return []interface{}{
		imap.RawString("CHARSET"), imap.RawString("UTF-8"),
		imap.RawString("X-GM-RAW"), bytes.NewBufferString(query),
	}
}

func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// searchHandler collects the numbers from untagged SEARCH responses.
type searchHandler struct {
	ids []uint32
}

func (h *searchHandler) Handle(resp imap.Resp) error {
	name, fields, ok := imap.ParseNamedResp(resp)
	if !ok || name != "SEARCH" {
		return responses.ErrUnhandled
	}
	for _, f := range fields {
		if n, err := imap.ParseNumber(f); err == nil {
			h.ids = append(h.ids, n)
		}
	}
	return nil
}

// fetchHandler collects FETCH responses for the requested set. It never
// aborts the command: unparseable or unrelated (unsolicited) responses are
// skipped.
type fetchHandler struct {
	set  *imap.SeqSet
	uid  bool
	keep func(*imap.Message) bool
	msgs []*imap.Message
	seen map[uint32]int
	err  error // first parse error, reported if nothing was collected
}

func (h *fetchHandler) Handle(resp imap.Resp) error {
	msg, ok, err := parseFetchResp(resp)
	if !ok {
		return responses.ErrUnhandled
	}
	if err != nil {
		if h.err == nil {
			h.err = err
		}
		return nil
	}
	key := msg.SeqNum
	if h.uid {
		key = msg.Uid
	}
	if key == 0 || !h.set.Contains(key) || (h.keep != nil && !h.keep(msg)) {
		return responses.ErrUnhandled
	}
	if h.seen == nil {
		h.seen = make(map[uint32]int)
	}
	if i, dup := h.seen[key]; dup {
		h.msgs[i] = msg
		return nil
	}
	h.seen[key] = len(h.msgs)
	h.msgs = append(h.msgs, msg)
	return nil
}

// parseFetchResp parses an untagged FETCH response. ok is false when resp is
// not a FETCH response at all.
func parseFetchResp(resp imap.Resp) (msg *imap.Message, ok bool, err error) {
	name, fields, isNamed := imap.ParseNamedResp(resp)
	if !isNamed || name != "FETCH" {
		return nil, false, nil
	}
	if len(fields) < 2 {
		return nil, true, fmt.Errorf("FETCH response without data")
	}
	seq, err := imap.ParseNumber(fields[0])
	if err != nil {
		return nil, true, fmt.Errorf("FETCH response: %w", err)
	}
	items, _ := fields[1].([]interface{})
	bodiesAsLiterals(items)

	msg = &imap.Message{SeqNum: seq}
	if err := msg.Parse(items); err != nil {
		// A body structure go-imap can't parse shouldn't cost us the rest of
		// the message; retry without it.
		msg = &imap.Message{SeqNum: seq}
		if err2 := msg.Parse(withoutItems(items, imap.FetchBodyStructure, imap.FetchBody)); err2 != nil {
			return nil, true, fmt.Errorf("FETCH response: %w", err)
		}
	}
	return msg, true, nil
}

// bodiesAsLiterals turns body sections sent as quoted strings into literals,
// which is the only form imap.Message keeps.
func bodiesAsLiterals(items []interface{}) {
	for i := 0; i+1 < len(items); i += 2 {
		key, _ := imap.ParseString(items[i])
		if s, ok := items[i+1].(string); ok && strings.HasPrefix(strings.ToUpper(key), "BODY[") {
			items[i+1] = bytes.NewBufferString(s)
		}
	}
}

func withoutItems(items []interface{}, drop ...imap.FetchItem) []interface{} {
	var out []interface{}
	for i := 0; i+1 < len(items); i += 2 {
		key, _ := imap.ParseString(items[i])
		skip := false
		for _, d := range drop {
			if strings.EqualFold(key, string(d)) {
				skip = true
			}
		}
		if !skip {
			out = append(out, items[i], items[i+1])
		}
	}
	return out
}

// fetch runs FETCH (or UID FETCH) and returns the collected messages in the
// order the server sent them.
func (c *Client) fetch(set *imap.SeqSet, uid bool, items []imap.FetchItem, keep func(*imap.Message) bool) ([]*imap.Message, error) {
	var cmd imap.Commander = &commands.Fetch{SeqSet: set, Items: items}
	if uid {
		cmd = &commands.Uid{Cmd: cmd}
	}
	h := &fetchHandler{set: set, uid: uid, keep: keep}
	if _, err := c.run(cmd, h); err != nil {
		return nil, err
	}
	if len(h.msgs) == 0 && h.err != nil {
		return nil, h.err
	}
	return h.msgs, nil
}

// storeUIDs runs UID STORE with a raw item name, e.g. "+X-GM-LABELS". The
// values are written as given: imap.RawString for atoms and flags, string for
// quoted strings. client.UidStore can't be used for X-GM-LABELS because it
// turns every string into an atom.
func (c *Client) storeUIDs(set *imap.SeqSet, item string, values []interface{}) error {
	cmd := &commands.Uid{Cmd: &commands.Store{SeqSet: set, Item: imap.StoreItem(item), Value: values}}
	_, err := c.run(cmd, nil)
	return err
}

// expungeUIDs permanently removes \Deleted messages, limited to set when the
// server has UIDPLUS.
func (c *Client) expungeUIDs(set *imap.SeqSet) error {
	if c.supports("UIDPLUS") {
		_, err := c.run(uidCommand("EXPUNGE", set), nil)
		return err
	}
	_, err := c.run(&commands.Expunge{}, nil)
	return err
}

func seqSetOf(uids []uint32) *imap.SeqSet {
	set := new(imap.SeqSet)
	set.AddNum(uids...)
	return set
}

func sortedUIDs(ids []uint32) []uint32 {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}
