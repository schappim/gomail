// Package htmltext converts HTML email bodies into readable plain text, the
// way a mail client's "plain text view" would show them.
//
// The converter parses the document with golang.org/x/net/html and walks the
// tree. Block elements produce line breaks, paragraphs and quotes are
// separated by blank lines, links keep their target in parentheses, lists get
// bullets or numbers, and blockquotes are rendered with "> " prefixes so
// quoted replies read naturally.
package htmltext

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// Convert renders HTML as readable plain text. It never panics: malformed or
// truncated markup is handled by the HTML5 parsing algorithm, and anything
// that still goes wrong falls back to a crude tag strip.
func Convert(src string) (out string) {
	defer func() {
		if r := recover(); r != nil {
			out = fallback(src)
		}
	}()
	if !utf8.ValidString(src) {
		src = strings.ToValidUTF8(src, "")
	}
	// Scripting disabled so <noscript> content is parsed as markup and shown,
	// which is what a mail client (which never runs scripts) displays.
	doc, err := html.ParseWithOptions(strings.NewReader(src), html.ParseOptionEnableScripting(false))
	if err != nil {
		return fallback(src)
	}
	r := &renderer{}
	r.walk(doc)
	return cleanup(r.buf.String())
}

// Elements whose content is never shown.
var skipTags = map[string]bool{
	"head": true, "title": true, "script": true, "style": true, "template": true,
	"meta": true, "link": true, "base": true,
	"svg": true, "math": true, "object": true, "embed": true, "iframe": true,
	"frame": true, "canvas": true, "audio": true, "video": true, "map": true,
	"input": true, "select": true, "textarea": true, "datalist": true,
	"param": true, "source": true, "track": true,
}

// Block elements that start and end on their own line.
var blockTags = map[string]bool{
	"address": true, "article": true, "aside": true, "center": true, "dd": true,
	"details": true, "dialog": true, "dir": true, "div": true, "dl": true,
	"dt": true, "fieldset": true, "figcaption": true, "figure": true,
	"footer": true, "form": true, "header": true, "hgroup": true, "legend": true,
	"main": true, "menu": true, "nav": true, "section": true, "summary": true,
	"caption": true, "tbody": true, "thead": true, "tfoot": true,
	"option": true, "optgroup": true, "frameset": true, "noframes": true,
}

// Block elements separated from their surroundings by a blank line.
var paragraphTags = map[string]bool{
	"p": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
}

// prefixEntry contributes a line prefix while its element is open: "> " for a
// blockquote, or a list marker for the first line of a list item followed by
// an indent for the item's continuation lines.
type prefixEntry struct {
	first string
	rest  string
	used  bool
}

type listState struct {
	ordered bool
	next    int
}

// renderer accumulates output. Line breaks are lazy: block boundaries only
// record how many newlines are wanted (pendingNL) and they are written when
// the next piece of content arrives, so empty layout elements never produce
// stray blank lines.
type renderer struct {
	buf      strings.Builder
	stack    []*prefixEntry
	lists    []*listState
	captures []*strings.Builder // text written inside open <a> elements

	started      bool // some content has been written
	lineOpen     bool // the current output line already has content
	pendingNL    int  // newlines to write before the next content
	pendingDepth int  // prefix depth used for pending blank lines
	needSpace    bool // collapsed whitespace is waiting to be written
	sep          string
	pre          int // depth of <pre> nesting
}

func (r *renderer) walk(n *html.Node) {
	switch n.Type {
	case html.DocumentNode:
		r.children(n)
	case html.ElementNode:
		r.element(n)
	case html.TextNode:
		if r.pre > 0 {
			r.preText(n.Data)
		} else {
			r.text(n.Data)
		}
	}
}

func (r *renderer) children(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		r.walk(c)
	}
}

func (r *renderer) element(n *html.Node) {
	tag := n.Data
	if skipTags[tag] || isHidden(n) {
		return
	}
	switch tag {
	case "br":
		r.hardBreak()
	case "hr":
		r.blockBreak(1)
		r.startContent()
		r.write("---")
		r.blockBreak(1)
	case "img":
		if alt := collapse(attr(n, "alt")); alt != "" {
			r.text("[image: " + alt + "]")
		}
	case "a":
		r.anchor(n)
	case "ul", "ol":
		r.list(n)
	case "li":
		r.listItem(n)
	case "blockquote":
		r.blockBreak(2)
		r.stack = append(r.stack, &prefixEntry{first: "> ", rest: "> "})
		r.children(n)
		r.stack = r.stack[:len(r.stack)-1]
		r.blockBreak(2)
	case "pre", "listing", "xmp", "plaintext":
		r.blockBreak(2)
		r.pre++
		r.children(n)
		r.pre--
		r.blockBreak(2)
	case "table", "tr":
		r.blockBreak(1)
		r.children(n)
		r.blockBreak(1)
	case "td", "th":
		// Cells on the same row are separated by two spaces. The separator is
		// only written if more content follows on the same line, so empty
		// layout cells vanish.
		if r.lineOpen && r.pendingNL == 0 {
			r.sep = "  "
		}
		r.children(n)
	default:
		switch {
		case paragraphTags[tag]:
			r.blockBreak(2)
			r.children(n)
			r.blockBreak(2)
		case blockTags[tag]:
			r.blockBreak(1)
			r.children(n)
			r.blockBreak(1)
		default:
			r.children(n)
		}
	}
}

func (r *renderer) anchor(n *html.Node) {
	href := strings.TrimSpace(attr(n, "href"))
	c := &strings.Builder{}
	r.captures = append(r.captures, c)
	r.children(n)
	r.captures = r.captures[:len(r.captures)-1]

	if !showHref(href) {
		return
	}
	text := collapse(c.String())
	if text == "" || sameURL(text, href) {
		return
	}
	if r.lineOpen && r.pendingNL == 0 {
		r.needSpace = true
	}
	r.startContent()
	r.write("(" + href + ")")
}

func (r *renderer) list(n *html.Node) {
	ls := &listState{ordered: n.Data == "ol", next: 1}
	if ls.ordered {
		if v, err := strconv.Atoi(strings.TrimSpace(attr(n, "start"))); err == nil {
			ls.next = v
		}
	}
	r.blockBreak(1)
	r.lists = append(r.lists, ls)
	r.children(n)
	r.lists = r.lists[:len(r.lists)-1]
	r.blockBreak(1)
}

func (r *renderer) listItem(n *html.Node) {
	marker := "- "
	if len(r.lists) > 0 {
		ls := r.lists[len(r.lists)-1]
		if ls.ordered {
			if v, err := strconv.Atoi(strings.TrimSpace(attr(n, "value"))); err == nil {
				ls.next = v
			}
			marker = strconv.Itoa(ls.next) + ". "
			ls.next++
		}
	}
	r.blockBreak(1)
	// Continuation lines and nested lists are indented two spaces per level.
	r.stack = append(r.stack, &prefixEntry{first: marker, rest: "  "})
	r.children(n)
	r.stack = r.stack[:len(r.stack)-1]
	r.blockBreak(1)
}

// blockBreak asks for at least n newlines before the next content (1 = new
// line, 2 = blank line).
func (r *renderer) blockBreak(n int) {
	r.needSpace = false
	r.sep = ""
	if !r.started {
		return
	}
	if r.pendingNL == 0 || len(r.stack) < r.pendingDepth {
		r.pendingDepth = len(r.stack)
	}
	if n > r.pendingNL {
		r.pendingNL = n
	}
}

// hardBreak is a <br> (or a newline inside <pre>): it always adds a line.
func (r *renderer) hardBreak() {
	r.needSpace = false
	r.sep = ""
	if !r.started {
		return
	}
	if r.pendingNL == 0 || len(r.stack) < r.pendingDepth {
		r.pendingDepth = len(r.stack)
	}
	r.pendingNL++
}

// startContent writes any pending line breaks, the line prefix for a new line,
// or the pending separator/space for a continued line.
func (r *renderer) startContent() {
	if r.pendingNL > 0 && r.started {
		blank := strings.TrimRight(r.prefix(r.pendingDepth, false), " ")
		for i := 0; i < r.pendingNL; i++ {
			if i > 0 {
				r.buf.WriteString(blank)
			}
			r.buf.WriteByte('\n')
		}
		r.lineOpen = false
		for _, c := range r.captures {
			c.WriteByte(' ')
		}
	}
	r.pendingNL = 0
	if !r.lineOpen {
		r.buf.WriteString(r.prefix(len(r.stack), true))
		r.lineOpen = true
		r.started = true
	} else if r.sep != "" {
		r.write(r.sep)
	} else if r.needSpace {
		r.write(" ")
	}
	r.sep = ""
	r.needSpace = false
}

func (r *renderer) prefix(depth int, consume bool) string {
	if depth > len(r.stack) {
		depth = len(r.stack)
	}
	var b strings.Builder
	for _, e := range r.stack[:depth] {
		if consume && !e.used {
			b.WriteString(e.first)
			e.used = true
		} else {
			b.WriteString(e.rest)
		}
	}
	return b.String()
}

// write appends content to the current line (and to open link captures).
func (r *renderer) write(s string) {
	r.buf.WriteString(s)
	for _, c := range r.captures {
		c.WriteString(s)
	}
}

// text writes normal flow text, collapsing whitespace per HTML rules.
func (r *renderer) text(s string) {
	if strings.ContainsRune(s, '\u200c') {
		s = stripZWNJ(s)
	}
	start := -1
	flush := func(end int) {
		if start >= 0 {
			r.startContent()
			r.write(s[start:end])
			start = -1
		}
	}
	for i, ch := range s {
		switch {
		case isSpace(ch):
			flush(i)
			if r.lineOpen && r.pendingNL == 0 {
				r.needSpace = true
			}
		case isInvisible(ch):
			flush(i)
		default:
			if start < 0 {
				start = i
			}
		}
	}
	flush(len(s))
}

// preText writes preformatted text verbatim, line by line.
func (r *renderer) preText(s string) {
	start := -1
	flush := func(end int) {
		if start >= 0 {
			r.startContent()
			r.write(strings.ReplaceAll(s[start:end], "\u00a0", " "))
			start = -1
		}
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\n':
			flush(i)
			r.hardBreak()
		case '\r':
			flush(i)
		default:
			if start < 0 {
				start = i
			}
		}
	}
	flush(len(s))
}

// isSpace reports collapsible whitespace. A non-breaking space is treated as
// an ordinary space in plain text.
func isSpace(ch rune) bool {
	switch ch {
	case ' ', '\t', '\n', '\r', '\f', '\v', '\u00a0':
		return true
	}
	return false
}

// isInvisible reports zero-width and formatting characters that marketing
// emails use as invisible padding; they carry no visible text.
func isInvisible(ch rune) bool {
	switch ch {
	case '\u200b', '\ufeff', '\u00ad', '\u034f', '\u2060', '\u180e':
		return true
	}
	return false
}

// stripZWNJ removes zero-width non-joiners except between two letters, where
// they are meaningful (e.g. in Persian).
func stripZWNJ(s string) string {
	rs := []rune(s)
	out := rs[:0:0]
	for i, ch := range rs {
		if ch == '\u200c' {
			if i == 0 || i == len(rs)-1 || !unicode.IsLetter(rs[i-1]) || !unicode.IsLetter(rs[i+1]) {
				continue
			}
		}
		out = append(out, ch)
	}
	return string(out)
}

// isHidden reports elements that are not displayed: the hidden attribute or
// an inline display:none (commonly used for marketing pre-header text).
func isHidden(n *html.Node) bool {
	for _, a := range n.Attr {
		switch a.Key {
		case "hidden":
			return true
		case "style":
			if strings.Contains(strings.Join(strings.Fields(strings.ToLower(a.Val)), ""), "display:none") {
				return true
			}
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func collapse(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return isSpace(r) || unicode.IsSpace(r) || isInvisible(r)
	}), " ")
}

// showHref reports whether a link target is worth printing.
func showHref(href string) bool {
	if href == "" || strings.HasPrefix(href, "#") {
		return false
	}
	return !strings.HasPrefix(strings.ToLower(href), "javascript:")
}

// sameURL reports whether the link text already shows the target.
func sameURL(text, href string) bool {
	return normURL(text) == normURL(href)
}

func normURL(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, p := range []string{"mailto:", "http://", "https://"} {
		s = strings.TrimPrefix(s, p)
	}
	s = strings.TrimPrefix(s, "www.")
	return strings.TrimRight(s, "/")
}

// cleanup strips trailing spaces, collapses runs of blank lines (including
// runs of identical bare quote lines such as ">") and trims the result.
func cleanup(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if len(out) > 0 && out[len(out)-1] == l && (l == "" || isQuoteOnly(l)) {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func isQuoteOnly(l string) bool {
	return strings.Trim(l, "> ") == ""
}

// fallback strips tags crudely; used only if the parser fails.
func fallback(s string) string {
	var b strings.Builder
	inTag := false
	for _, ch := range s {
		switch {
		case ch == '<':
			inTag = true
			b.WriteByte(' ')
		case ch == '>' && inTag:
			inTag = false
		case !inTag:
			b.WriteRune(ch)
		}
	}
	return collapse(html.UnescapeString(b.String()))
}
