// Package model holds the data types shared by every gomail layer: the IMAP
// client fills in the Gmail-specific fields, the MIME parser fills in the
// content, and the CLI renders them as text or JSON.
package model

import (
	"strings"
	"time"
)

// Address is a single mailbox: an optional display name plus an email address.
type Address struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

// String renders the address for display ("Name <email>" or just "email").
// It does not apply RFC 2047 encoding; use compose for wire formatting.
func (a Address) String() string {
	if a.Name == "" {
		return a.Email
	}
	return a.Name + " <" + a.Email + ">"
}

// FormatAddresses joins addresses for display.
func FormatAddresses(list []Address) string {
	parts := make([]string, len(list))
	for i, a := range list {
		parts[i] = a.String()
	}
	return strings.Join(parts, ", ")
}

// Label roles. A label's Role is set for Gmail system mailboxes, discovered via
// IMAP SPECIAL-USE attributes so localized names ("[Google Mail]/Gesendet")
// still resolve.
const (
	RoleInbox     = "inbox"
	RoleSent      = "sent"
	RoleDrafts    = "drafts"
	RoleAll       = "all"
	RoleTrash     = "trash"
	RoleSpam      = "spam"
	RoleStarred   = "starred"
	RoleImportant = "important"
)

// Label is a Gmail label as exposed over IMAP (a mailbox).
type Label struct {
	Name       string   `json:"name"`           // full mailbox name, UTF-8 decoded, e.g. "INBOX", "[Gmail]/Sent Mail", "Clients/Acme"
	Role       string   `json:"role,omitempty"` // one of the Role* constants, empty for user labels
	Attributes []string `json:"attributes,omitempty"`
	Selectable bool     `json:"selectable"`
	Messages   *uint32  `json:"messages,omitempty"` // only when counts were requested
	Unread     *uint32  `json:"unread,omitempty"`
}

// MessageSummary is what search and list return for each message.
type MessageSummary struct {
	ID        string    `json:"id"`                   // Gmail message id (X-GM-MSGID) in lowercase hex, matches the Gmail web UI
	ThreadID  string    `json:"thread_id"`            // Gmail thread id (X-GM-THRID) in lowercase hex
	MessageID string    `json:"message_id,omitempty"` // RFC 5322 Message-ID header, without angle brackets
	InReplyTo string    `json:"in_reply_to,omitempty"`
	Date      time.Time `json:"date"`
	From      []Address `json:"from"`
	To        []Address `json:"to,omitempty"`
	Cc        []Address `json:"cc,omitempty"`
	Bcc       []Address `json:"bcc,omitempty"`
	ReplyTo   []Address `json:"reply_to,omitempty"`
	Subject   string    `json:"subject"`
	Labels    []string  `json:"labels"`          // normalized: INBOX, SENT, DRAFT, IMPORTANT, STARRED, TRASH, SPAM, or user label names
	Flags     []string  `json:"flags,omitempty"` // raw IMAP flags, e.g. \Seen, \Flagged
	Unread    bool      `json:"unread"`
	Starred   bool      `json:"starred"`
	Size      uint32    `json:"size"`
	HasAttach bool      `json:"has_attachments"`
	Snippet   string    `json:"snippet,omitempty"`
	Mailbox   string    `json:"-"` // mailbox the summary was fetched from
	UID       uint32    `json:"-"` // UID within Mailbox
}

// Header is one raw header field, decoded to UTF-8.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Attachment is a non-body MIME part.
type Attachment struct {
	Index       int    `json:"index"` // 1-based, stable for a given message
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"` // decoded size in bytes
	ContentID   string `json:"content_id,omitempty"`
	Inline      bool   `json:"inline"`
	Data        []byte `json:"-"`
}

// Message is a fully parsed message.
type Message struct {
	MessageSummary
	References   []string     `json:"references,omitempty"`
	Headers      []Header     `json:"headers,omitempty"`
	Text         string       `json:"text"`                     // best plain-text body (text/plain, or derived from HTML)
	HTML         string       `json:"html,omitempty"`           // HTML body if present
	TextFromHTML bool         `json:"text_from_html,omitempty"` // Text was derived from HTML
	Attachments  []Attachment `json:"attachments"`
	Raw          []byte       `json:"-"`
}

// Header returns the first header value with the given name (case-insensitive).
func (m *Message) Header(name string) string {
	for _, h := range m.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}
