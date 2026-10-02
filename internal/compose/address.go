package compose

import (
	"fmt"
	"io"
	"mime"
	"net/mail"
	"strings"

	"github.com/emersion/go-message"

	"gomail/internal/model"
)

var addressParser = mail.AddressParser{WordDecoder: &mime.WordDecoder{CharsetReader: charsetReader}}

// charsetReader defers to go-message's CharsetReader, which is set when its
// charset package is linked in.
func charsetReader(charset string, r io.Reader) (io.Reader, error) {
	if message.CharsetReader != nil {
		return message.CharsetReader(charset, r)
	}
	return nil, fmt.Errorf("unhandled charset %q", charset)
}

// ParseAddressList parses a list of addresses as typed on a command line:
// "a@b.com", "Name <a@b.com>" or `"Last, First" <a@b.com>`, separated by
// commas or semicolons. Blank entries are ignored; an invalid entry is an
// error naming it.
func ParseAddressList(s string) ([]model.Address, error) {
	var out []model.Address
	for _, entry := range splitAddressList(s) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		a, err := parseAddress(entry)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// splitAddressList splits on commas, semicolons and newlines that aren't
// inside quotes, angle brackets or comments.
func splitAddressList(s string) []string {
	var parts []string
	var quoted, escaped bool
	angle, paren := 0, 0
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		switch {
		case c == '\\' && (quoted || paren > 0):
			escaped = true
		case c == '"' && paren == 0:
			quoted = !quoted
		case quoted:
		case c == '(':
			paren++
		case c == ')' && paren > 0:
			paren--
		case paren > 0:
		case c == '<':
			angle++
		case c == '>' && angle > 0:
			angle--
		case angle > 0:
		case c == ',' || c == ';' || c == '\n' || c == '\r':
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

// parseAddress parses one entry with net/mail, falling back to a looser
// "display name <email>" reading for names net/mail rejects (unquoted dots
// or @ signs, stray quotes).
func parseAddress(entry string) (model.Address, error) {
	if a, err := addressParser.Parse(entry); err == nil {
		return model.Address{Name: strings.TrimSpace(a.Name), Email: a.Address}, nil
	}
	if strings.HasSuffix(entry, ">") {
		if lt := strings.LastIndexByte(entry, '<'); lt >= 0 {
			email := strings.TrimSpace(entry[lt+1 : len(entry)-1])
			if a, err := addressParser.Parse("<" + email + ">"); err == nil {
				name := strings.TrimSpace(entry[:lt])
				if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
					name = strings.ReplaceAll(name[1:len(name)-1], `\"`, `"`)
				}
				if dec, err := addressParser.WordDecoder.DecodeHeader(name); err == nil {
					name = dec
				}
				return model.Address{Name: strings.TrimSpace(name), Email: a.Address}, nil
			}
		}
	}
	return model.Address{}, fmt.Errorf("invalid email address %q", entry)
}
