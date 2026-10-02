package compose

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/http"
	"path/filepath"
	"strings"

	"gomail/internal/model"
)

// part is a node of the MIME tree being built.
type part struct {
	mediaType string            // lowercase, e.g. "text/plain"
	params    map[string]string // Content-Type parameters, unencoded
	encoding  string            // Content-Transfer-Encoding of a leaf
	fields    []field           // further part header fields, in order
	body      []byte            // leaf content, before transfer encoding
	children  []*part
}

func (p *part) isMultipart() bool { return strings.HasPrefix(p.mediaType, "multipart/") }

// structure renders the compact MIME tree, e.g.
// "multipart/mixed(multipart/alternative(text/plain,text/html),application/pdf)".
func (p *part) structure() string {
	if !p.isMultipart() {
		return p.mediaType
	}
	kids := make([]string, len(p.children))
	for i, c := range p.children {
		kids[i] = c.structure()
	}
	return p.mediaType + "(" + strings.Join(kids, ",") + ")"
}

// headerFields returns the part's MIME header fields and, for a multipart,
// the fresh boundary it uses.
func (p *part) headerFields() (fields []field, boundary string, err error) {
	params := make(map[string]string, len(p.params)+1)
	for k, v := range p.params {
		if k == "name" {
			// RFC 2047 in the legacy name parameter, as Gmail and Thunderbird
			// do; Content-Disposition carries the RFC 2231 form.
			v = encodeText(v)
		}
		params[k] = v
	}
	if p.isMultipart() {
		if boundary, err = newBoundary(); err != nil {
			return nil, "", err
		}
		params["boundary"] = boundary
	}
	ct := mime.FormatMediaType(p.mediaType, params)
	if ct == "" {
		return nil, "", fmt.Errorf("invalid content type %q", p.mediaType)
	}
	fields = []field{{"Content-Type", ct}}
	if !p.isMultipart() && p.encoding != "" {
		fields = append(fields, field{"Content-Transfer-Encoding", p.encoding})
	}
	return append(fields, p.fields...), boundary, nil
}

// newBoundary returns a random multipart boundary. The "=_" prefix can never
// occur in quoted-printable or base64 output.
func newBoundary() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "=_" + hex.EncodeToString(b[:]), nil
}

// writeMessage writes the message header fields followed by the MIME tree.
func writeMessage(w io.Writer, top []field, root *part) error {
	bw := bufio.NewWriter(w)
	if err := writePart(bw, top, root); err != nil {
		return err
	}
	return bw.Flush()
}

// writePart writes a part's header (after any leading fields) and body. It
// writes the MIME framing itself so the field order and spelling are exactly
// as given and any charset parameter passes through untouched.
func writePart(w *bufio.Writer, leading []field, p *part) error {
	fields, boundary, err := p.headerFields()
	if err != nil {
		return err
	}
	for _, f := range append(append([]field{}, leading...), fields...) {
		w.Write(foldField(f.k, f.v))
	}
	w.WriteString("\r\n")

	if p.isMultipart() {
		for _, c := range p.children {
			w.WriteString("--" + boundary + "\r\n")
			if err := writePart(w, nil, c); err != nil {
				return err
			}
			// The CRLF before a boundary belongs to the delimiter.
			w.WriteString("\r\n")
		}
		_, err := w.WriteString("--" + boundary + "--\r\n")
		return err
	}

	switch p.encoding {
	case "base64":
		return writeBase64(w, p.body)
	case "quoted-printable":
		qp := quotedprintable.NewWriter(w)
		if _, err := qp.Write(p.body); err != nil {
			return err
		}
		return qp.Close()
	default:
		_, err := w.Write(p.body)
		return err
	}
}

// writeBase64 writes data base64-encoded in 76-character lines.
func writeBase64(w io.Writer, data []byte) error {
	const lineBytes = 57 // 76 encoded characters
	line := make([]byte, base64.StdEncoding.EncodedLen(lineBytes)+2)
	for len(data) > 0 {
		n := min(lineBytes, len(data))
		m := base64.StdEncoding.EncodedLen(n)
		base64.StdEncoding.Encode(line, data[:n])
		line[m], line[m+1] = '\r', '\n'
		if _, err := w.Write(line[:m+2]); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func textPart(mediaType, content string) *part {
	return &part{
		mediaType: mediaType,
		params:    map[string]string{"charset": "utf-8"},
		encoding:  "quoted-printable",
		body:      []byte(content),
	}
}

// buildTree lays out the MIME structure:
//
//	text/plain
//	multipart/alternative(text/plain, text/html)
//	multipart/alternative(text/plain, multipart/related(text/html, image...))
//	multipart/mixed(<any of the above>, attachment...)
func buildTree(b body, files []File) (*part, []model.Attachment, error) {
	var atts []model.Attachment
	add := func(parent *part, f File, disposition string) error {
		p, meta, err := filePart(f, disposition)
		if err != nil {
			return err
		}
		parent.children = append(parent.children, p)
		meta.Index = len(atts) + 1
		atts = append(atts, meta)
		return nil
	}

	hasHTML := b.html != ""
	var related, mixed []File
	for _, f := range files {
		if hasHTML && f.Inline && cleanMsgID(f.ContentID) != "" {
			related = append(related, f)
		} else {
			mixed = append(mixed, f)
		}
	}

	content := textPart("text/plain", b.text)
	if hasHTML {
		htmlNode := textPart("text/html", b.html)
		if len(related) > 0 {
			rel := &part{
				mediaType: "multipart/related",
				params:    map[string]string{"type": "text/html"},
				children:  []*part{htmlNode},
			}
			for _, f := range related {
				if err := add(rel, f, "inline"); err != nil {
					return nil, nil, err
				}
			}
			htmlNode = rel
		}
		content = &part{mediaType: "multipart/alternative", children: []*part{content, htmlNode}}
	}
	if len(mixed) == 0 {
		return content, atts, nil
	}

	root := &part{mediaType: "multipart/mixed", children: []*part{content}}
	for _, f := range mixed {
		disposition := "attachment"
		if f.Inline {
			disposition = "inline"
		}
		if err := add(root, f, disposition); err != nil {
			return nil, nil, err
		}
	}
	return root, atts, nil
}

// filePart builds the base64 leaf for an attachment or inline image.
func filePart(f File, disposition string) (*part, model.Attachment, error) {
	name := cleanFilename(f.Filename)
	ct := strings.TrimSpace(f.ContentType)
	if ct == "" {
		ct = DetectContentType(name, f.Data)
	}
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return nil, model.Attachment{}, fmt.Errorf("compose: attachment %q: invalid content type %q", name, f.ContentType)
	}
	if strings.HasPrefix(mediaType, "multipart/") || strings.HasPrefix(mediaType, "message/") {
		// RFC 2046 forbids base64 for these types; send them as opaque files
		// and let the filename tell the recipient's client what they are.
		mediaType, params = "application/octet-stream", nil
	}
	if params == nil {
		params = map[string]string{}
	}
	delete(params, "name")
	if name != "" {
		params["name"] = name
	}

	cid := cleanMsgID(f.ContentID)
	fields := []field{{"Content-Disposition", formatDisposition(disposition, name)}}
	if cid != "" {
		fields = append(fields, field{"Content-ID", "<" + cid + ">"})
	}
	p := &part{mediaType: mediaType, params: params, encoding: "base64", fields: fields, body: f.Data}
	meta := model.Attachment{
		Filename:    name,
		ContentType: mediaType,
		Size:        len(f.Data),
		ContentID:   cid,
		Inline:      disposition == "inline",
	}
	return p, meta, nil
}

// cleanFilename drops any directory part and control characters.
func cleanFilename(name string) string {
	name = strings.TrimSpace(oneLine(name))
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	return strings.TrimSpace(name)
}

// DetectContentType guesses a file's MIME type from its extension, falling
// back to sniffing its content.
func DetectContentType(filename string, data []byte) string {
	if ext := filepath.Ext(filename); ext != "" {
		if t := mime.TypeByExtension(ext); t != "" {
			return t
		}
		if t := mime.TypeByExtension(strings.ToLower(ext)); t != "" {
			return t
		}
	}
	return http.DetectContentType(data)
}

// formatDisposition renders a Content-Disposition value. Non-ASCII filenames
// use RFC 2231 encoding, split into continuations so long names can fold.
func formatDisposition(disposition, filename string) string {
	if filename == "" {
		return disposition
	}
	if isPrintableASCII(filename) {
		if s := mime.FormatMediaType(disposition, map[string]string{"filename": filename}); s != "" {
			return s
		}
	}
	enc := percentEncode2231(filename)
	const maxChunk = 50 // keeps each folded continuation line within 76 characters
	if len(enc) <= maxChunk {
		return disposition + "; filename*=utf-8''" + enc
	}
	var b strings.Builder
	b.WriteString(disposition)
	for n := 0; enc != ""; n++ {
		cut := min(maxChunk, len(enc))
		// Never split a %XX escape.
		if i := strings.LastIndexByte(enc[:cut], '%'); i >= 0 && i > cut-3 && cut < len(enc) {
			cut = i
		}
		fmt.Fprintf(&b, "; filename*%d*=", n)
		if n == 0 {
			b.WriteString("utf-8''")
		}
		b.WriteString(enc[:cut])
		enc = enc[cut:]
	}
	return b.String()
}

func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < ' ' || s[i] > '~' {
			return false
		}
	}
	return true
}

// percentEncode2231 encodes s for an RFC 2231 extended parameter value.
func percentEncode2231(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("!#$&+-.^_`|~", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0xf])
	}
	return b.String()
}
