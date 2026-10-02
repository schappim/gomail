package mailparse

import (
	"bytes"
	"mime"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-message/mail"
)

// maxDepth bounds multipart nesting; deeper parts are kept as opaque leaves.
const maxDepth = 40

// node is one MIME entity. Leaves keep their raw (still transfer-encoded)
// body; multiparts keep their children.
type node struct {
	fields     []rawField
	header     mail.Header
	mediaType  string
	params     map[string]string
	disp       string // "", "inline" or "attachment"
	dispParams map[string]string
	encoding   string
	body       []byte
	children   []*node
}

func (n *node) isLeaf() bool { return len(n.children) == 0 }

func (n *node) charset() string { return n.params["charset"] }

// filename returns the part's declared file name: Content-Disposition
// filename first, then Content-Type name.
func (n *node) filename() string {
	for _, v := range []string{n.dispParams["filename"], n.params["name"], n.dispParams["name"], n.params["filename"]} {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func (n *node) contentID() string {
	id := strings.TrimSpace(n.header.Get("Content-Id"))
	id = strings.TrimPrefix(id, "<")
	id = strings.TrimSuffix(id, ">")
	return strings.TrimSpace(id)
}

// parseEntity parses a header block and body into a node, recursing into
// multipart bodies. hint is the charset used for raw 8-bit header bytes.
func parseEntity(fields []rawField, body []byte, defaultType, hint string, depth int) *node {
	n := &node{fields: fields, header: buildHeader(fields), body: body}
	n.mediaType, n.params = parseParamHeader(n.header.Get("Content-Type"), hint)
	if n.mediaType == "" || !strings.Contains(n.mediaType, "/") {
		if n.mediaType == "text" {
			n.mediaType = "text/plain"
		} else {
			n.mediaType = defaultType
		}
	}
	if cs := n.charset(); cs != "" {
		hint = cs
	}
	disp, dparams := parseParamHeader(n.header.Get("Content-Disposition"), hint)
	switch disp {
	case "", "inline":
		n.disp = disp
	default:
		// RFC 2183: unrecognised dispositions are treated as attachment.
		n.disp = "attachment"
	}
	n.dispParams = dparams
	n.encoding = n.header.Get("Content-Transfer-Encoding")

	if !strings.HasPrefix(n.mediaType, "multipart/") {
		return n
	}
	if depth >= maxDepth {
		n.mediaType = "application/octet-stream"
		return n
	}
	childDefault := "text/plain"
	if n.mediaType == "multipart/digest" {
		childDefault = "message/rfc822"
	}
	parts := splitMultipartBody(body, n.params["boundary"])
	if len(parts) == 0 {
		// Multiparts must not be transfer-encoded, but some are.
		if enc := normalizeEncoding(n.encoding); enc == "base64" || enc == "quoted-printable" {
			parts = splitMultipartBody(decodeTransfer(body, enc, false), n.params["boundary"])
		}
	}
	if len(parts) == 0 {
		// Broken multipart (missing or wrong boundary): show the body as text.
		n.mediaType = "text/plain"
		return n
	}
	for _, p := range parts {
		f, b := splitHeader(p)
		n.children = append(n.children, parseEntity(f, b, childDefault, hint, depth+1))
	}
	n.body = nil
	return n
}

// splitMultipartBody splits with the declared boundary, falling back to a
// boundary sniffed from the body when none is declared or it does not match.
func splitMultipartBody(body []byte, boundary string) [][]byte {
	boundary = strings.TrimRight(boundary, " \t")
	var parts [][]byte
	if boundary != "" {
		parts = splitMultipart(body, boundary)
	}
	if len(parts) == 0 {
		if b := sniffBoundary(body); b != "" && b != boundary {
			parts = splitMultipart(body, b)
		}
	}
	return parts
}

// splitMultipart returns the parts between "--boundary" delimiter lines. The
// preamble and epilogue are discarded. A missing closing delimiter (truncated
// message) ends the last part at the end of the body.
func splitMultipart(body []byte, boundary string) [][]byte {
	dash := []byte("--" + boundary)
	var parts [][]byte
	partStart := -1
	pos := 0
	for pos < len(body) {
		next := len(body)
		end := len(body)
		if i := bytes.IndexByte(body[pos:], '\n'); i >= 0 {
			end = pos + i
			next = end + 1
		}
		line := body[pos:end]
		if bytes.HasPrefix(line, dash) {
			rest := line[len(dash):]
			closing := bytes.HasPrefix(rest, []byte("--"))
			if closing {
				rest = rest[2:]
			}
			if len(bytes.Trim(rest, " \t\r")) == 0 {
				if partStart >= 0 {
					stop := pos
					// The line break before a delimiter belongs to it.
					if stop > partStart && body[stop-1] == '\n' {
						stop--
						if stop > partStart && body[stop-1] == '\r' {
							stop--
						}
					}
					parts = append(parts, body[partStart:stop])
				}
				if closing {
					return parts
				}
				partStart = next
			}
		}
		pos = next
	}
	if partStart >= 0 && partStart <= len(body) {
		parts = append(parts, body[partStart:])
	}
	return parts
}

func isBoundaryChar(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
		strings.IndexByte("'()+_,-./:=?", c) >= 0
}

// sniffBoundary looks for a line that is shaped like a multipart delimiter
// and is either repeated or followed by a header field. It runs in one pass
// over the body so pathological input stays linear.
func sniffBoundary(body []byte) string {
	lines := bytes.Split(body, []byte("\n"))
	cands := make([]string, len(lines))
	counts := map[string]int{}
	for i, l := range lines {
		if b := boundaryShape(l); b != "" {
			cands[i] = b
			counts[b]++
		}
	}
	for i, b := range cands {
		if b == "" {
			continue
		}
		nextIsHeader := i+1 < len(lines) && isHeaderLine(bytes.TrimRight(lines[i+1], "\r"))
		if counts[b] >= 2 || nextIsHeader {
			return b
		}
	}
	return ""
}

// boundaryShape returns the boundary of a "--boundary" or "--boundary--"
// line, or "" if the line does not look like a delimiter.
func boundaryShape(l []byte) string {
	l = bytes.TrimRight(l, " \t\r")
	if len(l) < 3 || !bytes.HasPrefix(l, []byte("--")) {
		return ""
	}
	b := bytes.TrimSuffix(l[2:], []byte("--"))
	if len(b) == 0 || len(b) > 70 {
		return ""
	}
	for _, c := range b {
		if !isBoundaryChar(c) {
			return ""
		}
	}
	return string(b)
}

// ---- body selection ----

// group is one displayed body section: the plain and/or HTML rendering of
// the same content.
type group struct {
	plain []*node
	html  []*node
}

func (g group) hasPlain() bool { return len(g.plain) > 0 }
func (g group) hasHTML() bool  { return len(g.html) > 0 }

type selector struct {
	redundant map[*node]bool // unchosen renderings inside multipart/alternative
}

// isBodyLeaf reports whether a leaf can be shown as body text.
func isBodyLeaf(n *node) bool {
	if n.disp == "attachment" {
		return false
	}
	return n.mediaType == "text/plain" || n.mediaType == "text/html"
}

func (s *selector) selectBody(n *node) []group {
	if n.isLeaf() {
		if !isBodyLeaf(n) {
			return nil
		}
		if n.mediaType == "text/html" {
			return []group{{html: []*node{n}}}
		}
		return []group{{plain: []*node{n}}}
	}
	switch n.mediaType {
	case "multipart/alternative":
		return s.selectAlternative(n)
	case "multipart/related":
		return s.selectBody(relatedRoot(n))
	default:
		// mixed, signed, report, parallel, digest and unknown multiparts:
		// every inline text part is shown, in order.
		var out []group
		for _, c := range n.children {
			out = append(out, s.selectBody(c)...)
		}
		return out
	}
}

// selectAlternative picks the last (richest) child providing plain text and
// the last child providing HTML. Text renderings in the other children are
// redundant copies and are not reported as attachments.
func (s *selector) selectAlternative(n *node) []group {
	plainIdx, htmlIdx := -1, -1
	child := make([][]group, len(n.children))
	for i, c := range n.children {
		child[i] = s.selectBody(c)
		for _, g := range child[i] {
			if g.hasPlain() {
				plainIdx = i
			}
			if g.hasHTML() {
				htmlIdx = i
			}
		}
	}
	var out group
	for i, gs := range child {
		// Other text renderings of the same content (text/enriched, AMP's
		// text/x-amp-html, Apple's text/watch-html) are not attachments
		// either. Calendar data is real content and stays an attachment.
		if c := n.children[i]; c.isLeaf() && c.disp != "attachment" &&
			strings.HasPrefix(c.mediaType, "text/") && c.mediaType != "text/calendar" && len(gs) == 0 {
			s.redundant[c] = true
		}
		for _, g := range gs {
			if i == plainIdx {
				out.plain = append(out.plain, g.plain...)
			} else {
				s.markRedundant(g.plain)
			}
			if i == htmlIdx {
				out.html = append(out.html, g.html...)
			} else {
				s.markRedundant(g.html)
			}
		}
	}
	if !out.hasPlain() && !out.hasHTML() {
		return nil
	}
	return []group{out}
}

func (s *selector) markRedundant(nodes []*node) {
	for _, n := range nodes {
		s.redundant[n] = true
	}
}

// relatedRoot returns the root of a multipart/related: the part named by the
// start parameter, or the first part.
func relatedRoot(n *node) *node {
	if start := strings.Trim(strings.TrimSpace(n.params["start"]), "<>"); start != "" {
		for _, c := range n.children {
			if c.contentID() == start {
				return c
			}
		}
	}
	return n.children[0]
}

// leafText decodes a text/plain leaf to UTF-8 with LF line endings,
// un-flowing format=flowed text.
func leafText(n *node) string {
	s := decodeCharset(decodeTransfer(n.body, n.encoding, false), n.charset())
	s = normalizeNewlines(s)
	if strings.EqualFold(strings.TrimSpace(n.params["format"]), "flowed") {
		s = unflow(s, strings.EqualFold(strings.TrimSpace(n.params["delsp"]), "yes"))
	}
	return s
}

// leafHTML decodes a text/html leaf to UTF-8 with LF line endings.
func leafHTML(n *node) string {
	return normalizeNewlines(decodeHTMLCharset(decodeTransfer(n.body, n.encoding, false), n.charset()))
}

// joinSections concatenates body sections with a blank line between them.
func joinSections(parts []string) string {
	var out []string
	for _, p := range parts {
		p = strings.TrimRight(strings.TrimLeft(p, "\n"), " \t\n")
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n\n")
}

// unflow decodes RFC 3676 format=flowed text: lines ending in a space are
// joined with the next line of the same quote depth; space-stuffing is
// removed; with DelSp=yes the trailing space is deleted when joining.
func unflow(s string, delsp bool) string {
	lines := strings.Split(s, "\n")
	var out []string
	var cur strings.Builder
	curDepth := 0
	open := false
	flush := func() {
		if !open {
			return
		}
		text := cur.String()
		if curDepth > 0 {
			prefix := strings.Repeat(">", curDepth)
			if text != "" {
				prefix += " "
			}
			text = prefix + text
		}
		out = append(out, text)
		cur.Reset()
		open = false
	}
	for _, line := range lines {
		depth := 0
		for depth < len(line) && line[depth] == '>' {
			depth++
		}
		content := line[depth:]
		content = strings.TrimPrefix(content, " ") // space-stuffing
		if open && depth != curDepth {
			flush() // a quote depth change ends a paragraph
		}
		sig := content == "-- "
		if sig {
			flush()
		}
		flowed := !sig && strings.HasSuffix(content, " ")
		if flowed && delsp {
			content = content[:len(content)-1]
		}
		cur.WriteString(content)
		curDepth = depth
		open = true
		if !flowed {
			flush()
		}
	}
	flush()
	return strings.Join(out, "\n")
}

// ---- attachments ----

// commonExt maps media types to the extension used for unnamed attachments.
// It takes precedence over mime.ExtensionsByType, whose answer depends on the
// system's mime.types and is sometimes odd (".jfif" for image/jpeg).
var commonExt = map[string]string{
	"image/jpeg":                    ".jpg",
	"image/jpg":                     ".jpg",
	"image/pjpeg":                   ".jpg",
	"image/png":                     ".png",
	"image/gif":                     ".gif",
	"image/webp":                    ".webp",
	"image/svg+xml":                 ".svg",
	"image/bmp":                     ".bmp",
	"image/tiff":                    ".tiff",
	"image/heic":                    ".heic",
	"image/heif":                    ".heif",
	"image/x-icon":                  ".ico",
	"application/pdf":               ".pdf",
	"application/zip":               ".zip",
	"application/x-zip-compressed":  ".zip",
	"application/gzip":              ".gz",
	"application/x-gzip":            ".gz",
	"application/x-tar":             ".tar",
	"application/x-7z-compressed":   ".7z",
	"application/x-rar-compressed":  ".rar",
	"application/json":              ".json",
	"application/xml":               ".xml",
	"application/rtf":               ".rtf",
	"application/msword":            ".doc",
	"application/vnd.ms-excel":      ".xls",
	"application/vnd.ms-powerpoint": ".ppt",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   ".docx",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         ".xlsx",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": ".pptx",
	"application/vnd.oasis.opendocument.text":                                   ".odt",
	"application/vnd.oasis.opendocument.spreadsheet":                            ".ods",
	"application/vnd.oasis.opendocument.presentation":                           ".odp",
	"application/ics":                  ".ics",
	"application/pgp-signature":        ".asc",
	"application/pgp-encrypted":        ".asc",
	"application/pgp-keys":             ".asc",
	"application/pkcs7-signature":      ".p7s",
	"application/x-pkcs7-signature":    ".p7s",
	"application/pkcs7-mime":           ".p7m",
	"application/x-pkcs7-mime":         ".p7m",
	"application/octet-stream":         ".bin",
	"text/plain":                       ".txt",
	"text/html":                        ".html",
	"text/csv":                         ".csv",
	"text/calendar":                    ".ics",
	"text/vcard":                       ".vcf",
	"text/x-vcard":                     ".vcf",
	"text/xml":                         ".xml",
	"text/markdown":                    ".md",
	"text/rfc822-headers":              ".txt",
	"message/rfc822":                   ".eml",
	"message/global":                   ".eml",
	"message/delivery-status":          ".txt",
	"message/disposition-notification": ".txt",
	"audio/mpeg":                       ".mp3",
	"audio/mp4":                        ".m4a",
	"audio/wav":                        ".wav",
	"audio/x-wav":                      ".wav",
	"audio/ogg":                        ".ogg",
	"video/mp4":                        ".mp4",
	"video/quicktime":                  ".mov",
	"video/mpeg":                       ".mpeg",
	"video/webm":                       ".webm",
}

func extensionFor(mediaType string) string {
	if e, ok := commonExt[mediaType]; ok {
		return e
	}
	if exts, err := mime.ExtensionsByType(mediaType); err == nil && len(exts) > 0 {
		return exts[0]
	}
	return ""
}

// sanitizeFilename keeps only the base name and removes control characters.
func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "." || name == ".." {
		return ""
	}
	return name
}

// subjectFilename turns a subject into a safe file name stem.
func subjectFilename(subject string) string {
	s := strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || r == 0x7f:
			return ' '
		case strings.ContainsRune(`/\:*?"<>|`, r):
			return '_'
		}
		return r
	}, subject)
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(s, ". ")
	if utf8.RuneCountInString(s) > 100 {
		s = strings.TrimSpace(string([]rune(s)[:100]))
	}
	return s
}

// attachmentName picks the file name for an attachment.
func attachmentName(n *node, index int, data []byte) string {
	if name := sanitizeFilename(n.filename()); name != "" {
		return name
	}
	switch n.mediaType {
	case "message/rfc822", "message/global":
		f, _ := splitHeader(data)
		for _, fld := range f {
			if strings.EqualFold(fld.name, "Subject") {
				if s := subjectFilename(decodeHeaderText(fld.value, "")); s != "" {
					return s + ".eml"
				}
				break
			}
		}
		return "forwarded-message.eml"
	case "text/calendar", "application/ics":
		return "invite.ics"
	}
	return "attachment-" + strconv.Itoa(index) + extensionFor(n.mediaType)
}

// walkLeaves visits leaves in order of appearance.
func walkLeaves(n *node, fn func(*node)) {
	if n.isLeaf() {
		fn(n)
		return
	}
	for _, c := range n.children {
		walkLeaves(c, fn)
	}
}
