package gmail

import (
	"fmt"
	"sort"
	"strings"

	"github.com/emersion/go-imap"

	"gomail/internal/mailparse"
	"gomail/internal/model"
)

const (
	defaultLimit     = 25
	snippetFetchSize = 4096
	snippetRunes     = 200
)

// fetchedMessage is a summary plus the FETCH data it came from.
type fetchedMessage struct {
	summary model.MessageSummary
	msg     *imap.Message
}

// Search lists messages in a mailbox, optionally filtered with a Gmail query
// (X-GM-RAW), newest first.
func (c *Client) Search(opts SearchOptions) (*SearchResult, error) {
	name := opts.Mailbox
	if strings.TrimSpace(name) == "" {
		name = "all"
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	offset := max(opts.Offset, 0)

	mb, err := c.resolve(name)
	if err != nil {
		return nil, err
	}
	status, err := c.selectMailbox(mb, false)
	if err != nil {
		return nil, err
	}

	var (
		set   *imap.SeqSet
		uid   bool
		total int
	)
	if strings.TrimSpace(opts.Query) == "" {
		set, total = newestSeqRange(status.Messages, offset, limit)
	} else {
		uids, err := c.uidSearch(rawSearchCriteria(opts.Query)...)
		if err != nil {
			return nil, fmt.Errorf("search %q in %q: %w", opts.Query, mb.name, err)
		}
		set, total, uid = newestUIDs(uids, offset, limit), len(uids), true
	}

	res := &SearchResult{Mailbox: mb.name, Total: total, Messages: []model.MessageSummary{}}
	if set == nil {
		return res, nil
	}
	msgs, err := c.fetchSummaries(mb, set, uid, nil)
	if err != nil {
		return nil, err
	}
	sortNewestFirst(msgs)
	if opts.Snippets {
		if err := c.fillSnippets(msgs); err != nil {
			return nil, err
		}
	}
	for _, m := range msgs {
		res.Messages = append(res.Messages, m.summary)
	}
	return res, nil
}

// newestSeqRange pages by sequence number from the newest end of a mailbox
// with exists messages. It returns a nil set when the page is empty.
func newestSeqRange(exists uint32, offset, limit int) (*imap.SeqSet, int) {
	total := int(exists)
	hi := total - offset
	if hi < 1 {
		return nil, total
	}
	lo := max(hi-limit+1, 1)
	set := new(imap.SeqSet)
	set.AddRange(uint32(lo), uint32(hi))
	return set, total
}

// newestUIDs takes the page of the highest UIDs after skipping offset.
func newestUIDs(uids []uint32, offset, limit int) *imap.SeqSet {
	hi := len(uids) - offset
	if hi < 1 {
		return nil
	}
	lo := max(hi-limit, 0)
	return seqSetOf(uids[lo:hi])
}

// fetchSummaries fetches summaryItems (plus extra items) for set in the
// selected mailbox mb.
func (c *Client) fetchSummaries(mb *mailbox, set *imap.SeqSet, uid bool, extra []imap.FetchItem) ([]fetchedMessage, error) {
	items := append(append([]imap.FetchItem(nil), summaryItems...), extra...)
	msgs, err := c.fetch(set, uid, items, hasEnvelope)
	if err != nil {
		return nil, fmt.Errorf("fetch messages from %q: %w", mb.name, err)
	}
	out := make([]fetchedMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, fetchedMessage{summary: newSummary(m, mb), msg: m})
	}
	return out, nil
}

func sortNewestFirst(msgs []fetchedMessage) {
	sort.SliceStable(msgs, func(i, j int) bool {
		a, b := msgs[i].summary, msgs[j].summary
		if !a.Date.Equal(b.Date) {
			return a.Date.After(b.Date)
		}
		return a.UID > b.UID
	})
}

func sortOldestFirst(msgs []fetchedMessage) {
	sort.SliceStable(msgs, func(i, j int) bool {
		a, b := msgs[i].summary, msgs[j].summary
		if !a.Date.Equal(b.Date) {
			return a.Date.Before(b.Date)
		}
		return a.UID < b.UID
	})
}

// fillSnippets fetches the first 4 KiB of each message's best text part and
// decodes it. Messages whose text part has the same section path share one
// UID FETCH.
func (c *Client) fillSnippets(msgs []fetchedMessage) error {
	parts := make(map[uint32]textPart)
	groups := make(map[string][]uint32)
	for _, m := range msgs {
		if m.msg.BodyStructure == nil || m.summary.UID == 0 {
			continue
		}
		tp, ok := snippetPart(m.msg.BodyStructure)
		if !ok {
			continue
		}
		parts[m.summary.UID] = tp
		key := sectionPath(tp.path)
		groups[key] = append(groups[key], m.summary.UID)
	}

	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	snippets := make(map[uint32]string)
	for _, key := range keys {
		uids := groups[key]
		section := &imap.BodySectionName{
			BodyPartName: imap.BodyPartName{Path: parts[uids[0]].path},
			Peek:         true,
			Partial:      []int{0, snippetFetchSize},
		}
		fetched, err := c.fetch(seqSetOf(sortedUIDs(uids)), true, []imap.FetchItem{imap.FetchUid, section.FetchItem()}, nil)
		if err != nil {
			return fmt.Errorf("fetch snippets: %w", err)
		}
		for _, f := range fetched {
			tp, ok := parts[f.Uid]
			if !ok {
				continue
			}
			if data := bodyBytes(f, section); len(data) > 0 {
				snippets[f.Uid] = mailparse.DecodeSnippet(data, tp.encoding, tp.mediaType, tp.charset, snippetRunes)
			}
		}
	}
	for i := range msgs {
		msgs[i].summary.Snippet = snippets[msgs[i].summary.UID]
	}
	return nil
}
