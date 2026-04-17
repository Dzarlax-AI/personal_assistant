package telegram

import (
	"strings"
	"testing"
)

func TestMarkdown_PlainText(t *testing.T) {
	assertMD(t, "hello world", "hello world")
}

func TestMarkdown_Empty(t *testing.T) {
	assertMD(t, "", "")
}

// --- Headers ---

func TestMarkdown_H1(t *testing.T) {
	assertMD(t, "# Title", "<b><u>Title</u></b>")
}

func TestMarkdown_H2(t *testing.T) {
	assertMD(t, "## Section", "<b>Section</b>")
}

func TestMarkdown_H3(t *testing.T) {
	assertMD(t, "### Section", "<b><i>Section</i></b>")
}

func TestMarkdown_H6(t *testing.T) {
	assertMD(t, "###### Deep", "<b><i>Deep</i></b>")
}

// --- Bold ---

func TestMarkdown_BoldStars(t *testing.T) {
	assertMD(t, "**bold**", "<b>bold</b>")
}

func TestMarkdown_BoldUnderscores(t *testing.T) {
	assertMD(t, "__bold__", "<b>bold</b>")
}

// --- Italic ---

func TestMarkdown_ItalicStar(t *testing.T) {
	assertMD(t, "*italic*", "<i>italic</i>")
}

func TestMarkdown_ItalicUnderscore(t *testing.T) {
	assertMD(t, "_italic_", "<i>italic</i>")
}

// --- Strikethrough ---

func TestMarkdown_Strikethrough(t *testing.T) {
	assertMD(t, "~~strike~~", "<s>strike</s>")
}

// --- Inline code ---

func TestMarkdown_InlineCode(t *testing.T) {
	assertMD(t, "`code`", "<code>code</code>")
}

func TestMarkdown_InlineCodeProtectsContent(t *testing.T) {
	// Markdown inside inline code must NOT be formatted
	assertMD(t, "`**not bold**`", "<code>**not bold**</code>")
}

func TestMarkdown_InlineCodeEscapesHTML(t *testing.T) {
	assertMD(t, "`<div>`", "<code>&lt;div&gt;</code>")
}

// --- Links ---

func TestMarkdown_Link(t *testing.T) {
	assertMD(t, "[OpenAI](https://openai.com)", `<a href="https://openai.com">OpenAI</a>`)
}

// --- Fenced code blocks ---

func TestMarkdown_FencedCodeBlock(t *testing.T) {
	input := "```go\nfmt.Println(\"hi\")\n```"
	want := "<pre><code class=\"language-go\">fmt.Println(&#34;hi&#34;)</code></pre>"
	assertMD(t, input, want)
}

func TestMarkdown_FencedCodeBlockNoLang(t *testing.T) {
	input := "```\nsome code\n```"
	want := "<pre><code>some code</code></pre>"
	assertMD(t, input, want)
}

func TestMarkdown_FencedCodeBlockEscapesHTML(t *testing.T) {
	input := "```\n<script>alert(1)</script>\n```"
	want := "<pre><code>&lt;script&gt;alert(1)&lt;/script&gt;</code></pre>"
	assertMD(t, input, want)
}

// --- Lists ---

func TestMarkdown_BulletDash(t *testing.T) {
	assertMD(t, "- item", "• item")
}

func TestMarkdown_BulletStar(t *testing.T) {
	assertMD(t, "* item", "• item")
}

func TestMarkdown_NumberedList(t *testing.T) {
	assertMD(t, "1. first", "1. first")
}

func TestMarkdown_BulletListMultiline(t *testing.T) {
	input := "- one\n- two\n- three"
	want := "• one\n• two\n• three"
	assertMD(t, input, want)
}

// --- Checkboxes ---

func TestMarkdown_CheckboxUnchecked(t *testing.T) {
	assertMD(t, "- [ ] task", "☐ task")
}

func TestMarkdown_CheckboxChecked(t *testing.T) {
	assertMD(t, "- [x] done", "☑ done")
}

func TestMarkdown_CheckboxCheckedUpper(t *testing.T) {
	assertMD(t, "* [X] done", "☑ done")
}

func TestMarkdown_CheckboxWithBold(t *testing.T) {
	assertMD(t, "*   [ ] **Паспорт** (загран)", "☐ <b>Паспорт</b> (загран)")
}

// --- HTML escaping ---

func TestMarkdown_HTMLInText(t *testing.T) {
	assertMD(t, "use <b> tag", "use &lt;b&gt; tag")
}

func TestMarkdown_AmpersandEscaped(t *testing.T) {
	assertMD(t, "a & b", "a &amp; b")
}

// --- Mixed content ---

func TestMarkdown_BoldInHeader(t *testing.T) {
	assertMD(t, "## **Section**", "<b><b>Section</b></b>")
}

func TestMarkdown_InlineCodeInBoldNotFormatted(t *testing.T) {
	// Code span is extracted before bold, so it's protected
	got := markdownToTelegramHTML("**use `code` here**")
	if got == "" {
		t.Fatal("got empty result")
	}
	// Should contain <code>code</code> and <b>...</b>
	if !contains(got, "<code>code</code>") {
		t.Errorf("expected <code>code</code> in %q", got)
	}
}

func TestMarkdown_MultilineDocument(t *testing.T) {
	input := "# Title\n\nSome **bold** text.\n\n- item 1\n- item 2"
	got := markdownToTelegramHTML(input)
	if !contains(got, "<b><u>Title</u></b>") {
		t.Errorf("missing header in %q", got)
	}
	if !contains(got, "<b>bold</b>") {
		t.Errorf("missing bold in %q", got)
	}
	if !contains(got, "• item 1") {
		t.Errorf("missing bullet in %q", got)
	}
}

// --- Blockquote ---

func TestMarkdown_BlockquoteSingleLine(t *testing.T) {
	assertMD(t, "> quoted", "<blockquote>quoted</blockquote>")
}

func TestMarkdown_BlockquoteMultiLine(t *testing.T) {
	input := "> line one\n> line two"
	assertMD(t, input, "<blockquote>line one\nline two</blockquote>")
}

func TestMarkdown_BlockquoteInlineFormatting(t *testing.T) {
	assertMD(t, "> **bold** quote", "<blockquote><b>bold</b> quote</blockquote>")
}

func TestMarkdown_BlockquoteExpandableByLength(t *testing.T) {
	long := "> " + strings.Repeat("a", 320)
	got := markdownToTelegramHTML(long)
	if !strings.HasPrefix(got, "<blockquote expandable>") {
		t.Errorf("expected expandable blockquote, got %q", got)
	}
}

func TestMarkdown_BlockquoteExpandableByLines(t *testing.T) {
	input := "> a\n> b\n> c\n> d\n> e\n> f"
	got := markdownToTelegramHTML(input)
	if !strings.HasPrefix(got, "<blockquote expandable>") {
		t.Errorf("expected expandable blockquote, got %q", got)
	}
}

func TestMarkdown_BlockquoteEndsOnBlankLine(t *testing.T) {
	input := "> quoted\n\nnot quoted"
	got := markdownToTelegramHTML(input)
	if !contains(got, "<blockquote>quoted</blockquote>") {
		t.Errorf("blockquote not closed properly: %q", got)
	}
	if !contains(got, "not quoted") {
		t.Errorf("post-blockquote text missing: %q", got)
	}
}

// --- Table ---

func TestMarkdown_TableBasic(t *testing.T) {
	input := "| Name | Age |\n|------|-----|\n| Alice | 30 |\n| Bob  | 25 |"
	got := markdownToTelegramHTML(input)
	if !strings.HasPrefix(got, "<pre>") || !strings.HasSuffix(got, "</pre>") {
		t.Errorf("table not wrapped in <pre>: %q", got)
	}
	if !contains(got, "Name") || !contains(got, "Alice") || !contains(got, "30") {
		t.Errorf("table missing content: %q", got)
	}
	if !contains(got, "│") {
		t.Errorf("table missing column separator: %q", got)
	}
	if !contains(got, "─") {
		t.Errorf("table missing header separator: %q", got)
	}
}

func TestMarkdown_TableEscapesHTML(t *testing.T) {
	input := "| Tag |\n|---|\n| <b> |"
	got := markdownToTelegramHTML(input)
	if !contains(got, "&lt;b&gt;") {
		t.Errorf("table cell HTML not escaped: %q", got)
	}
}

// --- Horizontal rule ---

func TestMarkdown_HR_Dashes(t *testing.T) {
	assertMD(t, "---", hrLine)
}

func TestMarkdown_HR_Stars(t *testing.T) {
	assertMD(t, "***", hrLine)
}

func TestMarkdown_HR_Underscores(t *testing.T) {
	assertMD(t, "___", hrLine)
}

func TestMarkdown_HR_LongDashes(t *testing.T) {
	assertMD(t, "----------", hrLine)
}

// --- Spoilers ---

func TestMarkdown_Spoiler(t *testing.T) {
	assertMD(t, "||secret||", "<tg-spoiler>secret</tg-spoiler>")
}

func TestMarkdown_SpoilerWithFormatting(t *testing.T) {
	// Spoiler processed first, then bold inside the spoiler content still applies
	got := markdownToTelegramHTML("||**secret** answer||")
	if !contains(got, "<tg-spoiler>") || !contains(got, "<b>secret</b>") {
		t.Errorf("spoiler + bold not combined: %q", got)
	}
}

// --- Escape sequences ---

func TestMarkdown_EscapeAsterisk(t *testing.T) {
	assertMD(t, `\*not bold\*`, "*not bold*")
}

func TestMarkdown_EscapeUnderscore(t *testing.T) {
	assertMD(t, `\_not italic\_`, "_not italic_")
}

func TestMarkdown_EscapeBackslash(t *testing.T) {
	assertMD(t, `a \\ b`, `a \ b`)
}

func TestMarkdown_EscapePreventsBold(t *testing.T) {
	// \* should NOT start a bold run even with a real ** nearby
	got := markdownToTelegramHTML(`\*foo\* and **bar**`)
	if contains(got, "<b>foo") {
		t.Errorf("escaped asterisk started bold: %q", got)
	}
	if !contains(got, "<b>bar</b>") {
		t.Errorf("real bold missing: %q", got)
	}
}

// --- Footnotes ---

func TestMarkdown_FootnoteReference(t *testing.T) {
	got := markdownToTelegramHTML("See note[^1] here.")
	if !contains(got, "¹") {
		t.Errorf("footnote ref not converted to superscript: %q", got)
	}
}

func TestMarkdown_FootnoteDefinition(t *testing.T) {
	got := markdownToTelegramHTML("[^1]: first note")
	if !contains(got, "<i>¹ first note</i>") {
		t.Errorf("footnote def not rendered: %q", got)
	}
}

// --- splitMessage ---

func TestSplitMessage_Short(t *testing.T) {
	parts := splitMessage("hello", 100)
	if len(parts) != 1 || parts[0] != "hello" {
		t.Errorf("unexpected: %v", parts)
	}
}

func TestSplitMessage_ParagraphBoundary(t *testing.T) {
	text := "first paragraph\n\nsecond paragraph\n\nthird paragraph"
	parts := splitMessage(text, 30)
	if len(parts) < 2 {
		t.Errorf("expected multiple parts, got %d: %v", len(parts), parts)
	}
	// Verify no part exceeds maxLen
	for i, p := range parts {
		if len(p) > 30 {
			t.Errorf("part %d exceeds maxLen: %d chars", i, len(p))
		}
	}
}

func TestSplitMessage_LineBoundary(t *testing.T) {
	text := "line one\nline two\nline three\nline four"
	parts := splitMessage(text, 20)
	if len(parts) < 2 {
		t.Errorf("expected multiple parts, got %d: %v", len(parts), parts)
	}
	for i, p := range parts {
		if len(p) > 20 {
			t.Errorf("part %d exceeds maxLen: %d chars", i, len(p))
		}
	}
}

func TestSplitMessage_ClosesOpenFence(t *testing.T) {
	// Long fenced block forced to split: the first chunk must end with ```
	// and the second must start with ```go (same language).
	code := strings.Repeat("fmt.Println(\"hi\")\n", 40) // ~720 chars of body
	text := "intro paragraph\n\n```go\n" + code + "```\n\noutro paragraph"
	parts := splitMessage(text, 300)
	if len(parts) < 2 {
		t.Fatalf("expected multiple parts, got %d", len(parts))
	}
	// Every part must have balanced fences.
	for i, p := range parts {
		if strings.Count(p, "```")%2 != 0 {
			t.Errorf("part %d has unbalanced fences:\n%s", i, p)
		}
	}
	// The reopened fence should carry the language tag.
	if !strings.Contains(parts[1], "```go") {
		t.Errorf("second part missing reopened language fence: %q", parts[1])
	}
}

func TestSplitMessage_ShortFenceNotTouched(t *testing.T) {
	text := "a\n\n```\nshort\n```\n\nb"
	parts := splitMessage(text, 1000)
	if len(parts) != 1 {
		t.Errorf("short text should not split: %v", parts)
	}
}

// --- stripHTMLTags ---

func TestStripHTMLTags_KeepsUnicodeStructure(t *testing.T) {
	in := "<b>Title</b>\n• <i>item</i>\n<pre>code</pre>"
	got := stripHTMLTags(in)
	want := "Title\n• item\ncode"
	if got != want {
		t.Errorf("\n got: %q\nwant: %q", got, want)
	}
}

func TestStripHTMLTags_DecodesEntities(t *testing.T) {
	got := stripHTMLTags("a &amp; b &lt;c&gt;")
	want := "a & b <c>"
	if got != want {
		t.Errorf("\n got: %q\nwant: %q", got, want)
	}
}

// --- helpers ---

func assertMD(t *testing.T, input, want string) {
	t.Helper()
	got := markdownToTelegramHTML(input)
	if got != want {
		t.Errorf("\ninput: %q\n  got: %q\n want: %q", input, got, want)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
