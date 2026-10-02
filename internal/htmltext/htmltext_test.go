package htmltext

import (
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestConvert(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain text without tags", "Just some text", "Just some text"},
		{"paragraphs", "<p>First</p><p>Second</p>", "First\n\nSecond"},
		{"br", "Line one<br>Line two<br/>Line three", "Line one\nLine two\nLine three"},
		{"double br makes blank line", "a<br><br>b", "a\n\nb"},
		{"trailing br in block adds nothing", "<div>a<br></div><div>b</div>", "a\nb"},
		{"gmail empty line div", "<div>a</div><div><br></div><div>b</div>", "a\n\nb"},
		{"divs are lines", "<div>one</div><div>two</div>", "one\ntwo"},
		{"whitespace collapses", "<p>  lots   of\n\t space\r\n here  </p>", "lots of space here"},
		{"inline elements join", "<b>Hello</b> <i>world</i><span>!</span>", "Hello world!"},
		{"no space between adjacent inline", "<span>Hello</span><span>World</span>", "HelloWorld"},
		{"entities", "<p>&amp; &lt;tag&gt; &quot;q&quot; &#8217; &euro; &copy;</p>", "& <tag> \"q\" ’ € ©"},
		{"nbsp becomes space", "a&nbsp;b&#160;c", "a b c"},
		{"nbsp-only paragraphs vanish", "<p>a</p><p>&nbsp;</p><p>&nbsp;</p><p>b</p>", "a\n\nb"},
		{"headings", "<h1>Title</h1><p>Body</p><h2>Sub</h2>text", "Title\n\nBody\n\nSub\n\ntext"},
		{"hr", "above<hr>below", "above\n---\nbelow"},
		{"head title script style removed",
			"<html><head><title>Subject</title><style>p{color:red}</style><script>alert(1)</script></head><body><p>Hi</p><script>var x;</script><template><p>tpl</p></template></body></html>",
			"Hi"},
		{"noscript content shown", "<noscript><p>No JS</p></noscript>", "No JS"},
		{"comments and conditional comments dropped",
			"<p>a<!-- hidden --></p><!--[if mso]><p>outlook only</p><![endif]--><p>b</p>", "a\n\nb"},
		{"crlf in text", "<p>one\r\ntwo</p>", "one two"},
		{"invisible preheader padding removed", "<p>Hi\u200c\u00a0\u200c\u00a0\u034f \u200b\ufeffthere</p>", "Hi there"},
		{"zwnj between letters kept", "می\u200cخواهم", "می\u200cخواهم"},
		{"form controls skipped", "<form>Name: <input value=x><select><option>A</option></select><textarea>t</textarea></form>", "Name:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Convert(tt.in); got != tt.want {
				t.Errorf("Convert(%q)\n got: %q\nwant: %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestLinks(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"text and url", `Visit <a href="https://example.com/page">our site</a> today`, "Visit our site (https://example.com/page) today"},
		{"text equals url", `<a href="https://example.com/x">https://example.com/x</a>`, "https://example.com/x"},
		{"text is url without scheme", `<a href="https://example.com/">example.com</a>`, "example.com"},
		{"text is url without www", `<a href="http://www.example.com">example.com</a>`, "example.com"},
		{"mailto equals text", `Mail <a href="mailto:bob@example.com">bob@example.com</a>.`, "Mail bob@example.com."},
		{"mailto with name", `<a href="mailto:bob@example.com">Bob</a>`, "Bob (mailto:bob@example.com)"},
		{"empty href", `<a href="">click</a>`, "click"},
		{"fragment href", `<a href="#top">Back to top</a>`, "Back to top"},
		{"javascript href", `<a href="JavaScript:void(0)">Do it</a>`, "Do it"},
		{"no href", `<a name="x">anchor</a>`, "anchor"},
		{"nested markup", `<a href="https://x.test/a"><b>Bold</b> <i>link</i></a>`, "Bold link (https://x.test/a)"},
		{"image link with alt", `<a href="https://x.test/"><img src="logo.png" alt="Acme"></a>`, "[image: Acme] (https://x.test/)"},
		{"image link without alt", `<a href="https://x.test/"><img src="logo.png"></a>after`, "after"},
		{"entity in href", `<a href="https://x.test/?a=1&amp;b=2">q</a>`, "q (https://x.test/?a=1&b=2)"},
		{"href whitespace trimmed", `<a href="  https://x.test/  ">go</a>`, "go (https://x.test/)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Convert(tt.in); got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestImages(t *testing.T) {
	got := Convert(`<p>Logo: <img src="a.png" alt="  Acme   Corp "> <img src="pixel.gif" alt=""><img src="p2.gif" width=1 height=1></p>`)
	if want := "Logo: [image: Acme Corp]"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLists(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"ul", "<ul><li>One</li><li>Two</li></ul>", "- One\n- Two"},
		{"ol", "<ol><li>One</li><li>Two</li></ol>", "1. One\n2. Two"},
		{"ol start and value", `<ol start="5"><li>five</li><li value="9">nine</li><li>ten</li></ol>`, "5. five\n9. nine\n10. ten"},
		{"nested", "<ul><li>A<ul><li>A1</li><li>A2<ol><li>deep</li></ol></li></ul></li><li>B</li></ul>",
			"- A\n  - A1\n  - A2\n    1. deep\n- B"},
		{"nested ol in ol", "<ol><li>First<ol><li>Sub one</li><li>Sub two</li></ol></li><li>Second</li></ol>",
			"1. First\n  1. Sub one\n  2. Sub two\n2. Second"},
		{"li continuation via br", "<ul><li>Line one<br>line two</li></ul>", "- Line one\n  line two"},
		{"li with paragraph", "<ul><li><p>Para item</p></li></ul>", "- Para item"},
		{"list after text", "Intro<ul><li>x</li></ul>Outro", "Intro\n- x\nOutro"},
		{"empty items vanish", "<ul><li></li><li>real</li><li> </li></ul>", "- real"},
		{"li without list", "<li>orphan</li>", "- orphan"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Convert(tt.in); got != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestBlockquotes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"simple", "<p>Reply</p><blockquote>Original</blockquote>", "Reply\n\n> Original"},
		{"multi paragraph", "<blockquote><p>One</p><p>Two</p></blockquote>", "> One\n>\n> Two"},
		{"br inside", "<blockquote>a<br>b</blockquote>", "> a\n> b"},
		{"nested", "<blockquote>outer<blockquote>inner</blockquote>back</blockquote>after",
			"> outer\n>\n> > inner\n>\n> back\n\nafter"},
		{"list in quote", "<blockquote><ul><li>x</li><li>y</li></ul></blockquote>", "> - x\n> - y"},
		{"pre in quote", "<blockquote><pre>  a\n    b</pre></blockquote>", ">   a\n>     b"},
		{"gmail reply",
			`<div dir="ltr">Sounds good, see you then.</div><br><div class="gmail_quote"><div dir="ltr" class="gmail_attr">On Mon, 1 Jan 2024 at 10:00, Bob Smith &lt;<a href="mailto:bob@example.com">bob@example.com</a>&gt; wrote:<br></div><blockquote class="gmail_quote" style="margin:0px 0px 0px 0.8ex;border-left:1px solid rgb(204,204,204);padding-left:1ex"><div dir="ltr">Lunch on Friday?<div><br></div><div>Bob</div></div><br><div class="gmail_quote"><div class="gmail_attr">On Sun, Alice wrote:<br></div><blockquote class="gmail_quote">Are you free?</blockquote></div></blockquote></div>`,
			"Sounds good, see you then.\n\nOn Mon, 1 Jan 2024 at 10:00, Bob Smith <bob@example.com> wrote:\n\n> Lunch on Friday?\n>\n> Bob\n>\n> On Sun, Alice wrote:\n>\n> > Are you free?"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Convert(tt.in); got != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestTables(t *testing.T) {
	data := `<table><tr><th>Item</th><th>Qty</th><th>Price</th></tr>
<tr><td>Widget</td><td>2</td><td>$10.00</td></tr>
<tr><td>Gadget</td><td></td><td>$5.00</td></tr></table>`
	if got, want := Convert(data), "Item  Qty  Price\nWidget  2  $10.00\nGadget  $5.00"; got != want {
		t.Errorf("data table:\n got %q\nwant %q", got, want)
	}

	// A typical marketing layout: nested tables, spacer rows and cells,
	// empty cells, paragraphs inside cells.
	layout := `<table width="100%" cellpadding="0" cellspacing="0"><tr><td align="center">
  <table width="600"><tr><td height="20">&nbsp;</td></tr>
    <tr><td></td><td><img src="logo.png" alt="Acme"></td><td></td></tr>
    <tr><td height="30" style="font-size:0">&nbsp;</td></tr>
    <tr><td><table><tr><td><h1>Big Sale</h1></td></tr><tr><td><p>Everything 50% off.</p></td></tr></table></td></tr>
    <tr><td>&nbsp;</td></tr><tr><td>&nbsp;</td></tr><tr><td>&nbsp;</td></tr>
    <tr><td><a href="https://shop.test/">Shop now</a></td></tr>
  </table>
</td></tr></table>`
	want := "[image: Acme]\n\nBig Sale\n\nEverything 50% off.\n\nShop now (https://shop.test/)"
	if got := Convert(layout); got != want {
		t.Errorf("layout table:\n got %q\nwant %q", got, want)
	}
}

func TestPre(t *testing.T) {
	in := "<p>Code:</p><pre>func main() {\n\tfmt.Println(\"hi\")   \n\n    return\n}</pre><p>done</p>"
	want := "Code:\n\nfunc main() {\n\tfmt.Println(\"hi\")\n\n    return\n}\n\ndone"
	if got := Convert(in); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
	// Markup inside pre still renders, whitespace kept.
	if got, want := Convert("<pre>a  <b>bold</b>  z</pre>"), "a  bold  z"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestHidden(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"preheader", `<div style="display:none;max-height:0;overflow:hidden">Don't miss our sale! &zwnj;&nbsp;&zwnj;&nbsp;</div><p>Hello</p>`, "Hello"},
		{"spaced and uppercase", `<span style="color:red; DISPLAY : NONE !important">secret</span>shown`, "shown"},
		{"hidden attribute", `<div hidden>secret</div><p>shown</p>`, "shown"},
		{"display block is visible", `<div style="display:block">shown</div>`, "shown"},
		{"mso conditional hidden plus display none", `<div style="mso-hide:all;display: none">x</div>y`, "y"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Convert(tt.in); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOutlookStyle(t *testing.T) {
	in := `<html xmlns:o="urn:schemas-microsoft-com:office:office"><head><style><!-- p.MsoNormal {margin:0cm} --></style></head>
<body lang=EN-US><div class=WordSection1><p class=MsoNormal>Hi Bob,<o:p></o:p></p><p class=MsoNormal><o:p>&nbsp;</o:p></p>
<p class=MsoNormal>Please see attached.<o:p></o:p></p><p class=MsoNormal><o:p>&nbsp;</o:p></p><p class=MsoNormal>Thanks,<o:p></o:p></p>
<p class=MsoNormal>Alice<o:p></o:p></p></div></body></html>`
	want := "Hi Bob,\n\nPlease see attached.\n\nThanks,\n\nAlice"
	if got := Convert(in); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestMalformed(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"unclosed tags", "<p>unclosed <b>bold <i>italic", "unclosed bold italic"},
		{"misnested", "<b><i>one</b> two</i>", "one two"},
		{"stray end tags", "</div></p>text</span></table>", "text"},
		{"truncated tag", `Hello <a href="https://exa`, "Hello"},
		{"truncated attribute name", `Hello <img alt`, "Hello"},
		{"truncated entity", "fish &amp chips &am", "fish & chips &am"},
		{"lone angle brackets", "a < b > c", "a < b > c"},
		{"unterminated comment", "before<!-- never ends", "before"},
		{"unterminated style", "<p>x</p><style>p { color:", "x"},
		{"table soup (foster parenting, as browsers do)", "<table><td>a<tr>b<td>c</table>", "b\na\nc"},
		{"invalid utf8", "caf\xe9 ok", "caf ok"},
		{"null bytes", "a\x00b", "ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Convert(tt.in)
			if got != tt.want {
				t.Errorf("Convert(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDeepNesting(t *testing.T) {
	in := strings.Repeat("<div><blockquote>", 3000) + "deep" + strings.Repeat("</blockquote></div>", 3000)
	got := Convert(in)
	if !strings.HasSuffix(got, "deep") {
		t.Errorf("deep nesting lost text: %q", got[max(0, len(got)-50):])
	}
}

// TestNeverPanics feeds random fragments of tag soup to Convert.
func TestNeverPanics(t *testing.T) {
	pieces := []string{"<p>", "</p>", "<div>", "</div>", "<ul>", "<li>", "</ul>", "<ol start=x>", "<table>", "<tr>",
		"<td>", "</td>", "<pre>", "</pre>", "<blockquote>", "</blockquote>", "<a href='http://x'>", "</a>",
		"<img alt=pic>", "<br>", "<hr>", "text", " ", "\n", "&nbsp;", "&amp", "<", ">", "\"", "<!--", "-->",
		"<style>", "</style>", "<script>", "<span style='display:none'>", "</span>", "\u200c", "\xff", "<li value=-1>"}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 3000; i++ {
		var b strings.Builder
		for j := rng.Intn(40); j >= 0; j-- {
			b.WriteString(pieces[rng.Intn(len(pieces))])
		}
		in := b.String()
		out := Convert(in)
		if !utf8.ValidString(out) {
			t.Fatalf("invalid UTF-8 output for %q", in)
		}
		if strings.Contains(out, "\n\n\n") || out != strings.TrimSpace(out) {
			t.Fatalf("cleanup failed for %q: %q", in, out)
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.TrimRight(line, " \t") != line {
				t.Fatalf("trailing space in %q (input %q)", line, in)
			}
		}
	}
}

func FuzzConvert(f *testing.F) {
	for _, s := range []string{"", "<p>x</p>", "<ul><li>a<ol><li>b", "<blockquote><pre>x\n y</pre>", "<table><tr><td>a<td>b", `<a href="u">t</a>`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := Convert(s)
		if !utf8.ValidString(out) {
			t.Fatalf("invalid UTF-8 output")
		}
	})
}
