package compose

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"

	"github.com/emersion/go-message/textproto"

	"gomail/internal/model"
)

// PrepareForSend readies an existing raw message (e.g. a saved draft) for
// SMTP submission: it collects envelope recipients from To/Cc/Bcc, strips the
// Bcc header, refreshes Date and adds a Message-ID if there isn't one. Only
// the header is rewritten; the body bytes are passed through untouched.
func PrepareForSend(raw []byte) (out []byte, from string, rcpts []string, err error) {
	r := bytes.NewReader(raw)
	br := bufio.NewReader(r)
	h, err := textproto.ReadHeader(br)
	if err != nil {
		return nil, "", nil, fmt.Errorf("compose: reading message header: %w", err)
	}
	body := raw[len(raw)-r.Len()-br.Buffered():]

	fromList, err := headerAddresses(h, "From")
	if err != nil {
		return nil, "", nil, err
	}
	if len(fromList) == 0 {
		return nil, "", nil, fmt.Errorf("compose: the message has no From address")
	}
	from = fromList[0].Email

	for _, k := range []string{"To", "Cc", "Bcc"} {
		list, err := headerAddresses(h, k)
		if err != nil {
			return nil, "", nil, err
		}
		for _, a := range list {
			rcpts = append(rcpts, a.Email)
		}
	}
	rcpts = dedupeFold(rcpts)
	if len(rcpts) == 0 {
		return nil, "", nil, fmt.Errorf("compose: the message has no recipients (To, Cc or Bcc)")
	}

	// Keep the message's own line endings (the header parser hands back
	// fields with CRLF).
	nl := []byte("\r\n")
	if i := bytes.IndexByte(raw, '\n'); i >= 0 && (i == 0 || raw[i-1] != '\r') {
		nl = []byte("\n")
	}
	line := func(b []byte) []byte {
		return bytes.ReplaceAll(b, []byte("\r\n"), nl)
	}

	msgID := ""
	for fs := h.FieldsByKey("Message-Id"); fs.Next(); {
		if id := cleanMsgID(fs.Value()); id != "" {
			msgID = id
			break
		}
	}

	var buf bytes.Buffer
	buf.Grow(len(raw) + 128)
	buf.Write(line(foldField("Date", now().Format(dateLayout))))
	if msgID == "" {
		id, err := generateMessageID(from)
		if err != nil {
			return nil, "", nil, err
		}
		buf.Write(line(foldField("Message-ID", "<"+id+">")))
	}
	for fs := h.Fields(); fs.Next(); {
		switch strings.ToLower(fs.Key()) {
		case "bcc", "date":
			continue
		case "message-id":
			if msgID == "" {
				continue // empty or malformed; replaced above
			}
		}
		rawField, err := fs.Raw()
		if err != nil {
			return nil, "", nil, fmt.Errorf("compose: header %s: %w", fs.Key(), err)
		}
		buf.Write(line(rawField))
	}
	buf.Write(nl)
	buf.Write(body)
	return buf.Bytes(), from, rcpts, nil
}

// headerAddresses parses every occurrence of an address header, decoding
// RFC 2047 names. Values net/mail rejects get the tolerant command-line
// parser.
func headerAddresses(h textproto.Header, key string) ([]model.Address, error) {
	var out []model.Address
	for fs := h.FieldsByKey(key); fs.Next(); {
		v := strings.TrimSpace(fs.Value())
		if v == "" {
			continue
		}
		list, err := addressParser.ParseList(v)
		if err == nil {
			for _, a := range list {
				out = append(out, model.Address{Name: a.Name, Email: a.Address})
			}
			continue
		}
		if dec, derr := addressParser.WordDecoder.DecodeHeader(v); derr == nil {
			v = dec
		}
		loose, lerr := ParseAddressList(v)
		if lerr != nil {
			return nil, fmt.Errorf("compose: %s header: %w", key, lerr)
		}
		out = append(out, loose...)
	}
	return out, nil
}
