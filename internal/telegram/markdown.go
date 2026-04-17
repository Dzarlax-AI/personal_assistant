package telegram

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

var (
	reFenceOpen    = regexp.MustCompile("^```(\\w*)")
	reInlineCode   = regexp.MustCompile("`([^`\n]+)`")
	reBoldStar     = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reBoldUnder    = regexp.MustCompile(`__(.+?)__`)
	reItalicStar   = regexp.MustCompile(`\*([^*\n]+)\*`)
	reItalicUnder  = regexp.MustCompile(`_([^_\n]+)_`)
	reStrike       = regexp.MustCompile(`~~(.+?)~~`)
	reSpoiler      = regexp.MustCompile(`\|\|(.+?)\|\|`)
	reLink         = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	reHeader       = regexp.MustCompile(`^(#{1,6})\s+(.+)$`)
	reBulletItem   = regexp.MustCompile(`^(\s*)[-*]\s+(.+)$`)
	reNumItem      = regexp.MustCompile(`^(\s*\d+\.)\s+(.+)$`)
	reCheckbox     = regexp.MustCompile(`^\[[ ]\]\s*`)
	reCheckboxDone = regexp.MustCompile(`^\[[xX]\]\s*`)
	reHR           = regexp.MustCompile(`^\s*(?:(?:-\s*){3,}|(?:\*\s*){3,}|(?:_\s*){3,})$`)
	reBlockquote   = regexp.MustCompile(`^>\s?(.*)$`)
	reTableSep     = regexp.MustCompile(`^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)+\|?\s*$`)
	reTableRow     = regexp.MustCompile(`^\s*\|.*\|\s*$`)
	reFootnoteRef  = regexp.MustCompile(`\[\^([\w-]+)\]`)
	reFootnoteDef  = regexp.MustCompile(`^\[\^([\w-]+)\]:\s*(.+)$`)
	reEscape       = regexp.MustCompile("\\\\([\\\\*_~`\\[\\]|])")
	rePlaceholder  = regexp.MustCompile(`\x00(\d+)\x00`)
)

const hrLine = "──────────"

var supDigits = map[rune]rune{
	'0': '⁰', '1': '¹', '2': '²', '3': '³', '4': '⁴',
	'5': '⁵', '6': '⁶', '7': '⁷', '8': '⁸', '9': '⁹',
}

// markdownToTelegramHTML converts LLM Markdown output to Telegram-compatible HTML.
func markdownToTelegramHTML(src string) string {
	var sb strings.Builder
	lines := strings.Split(src, "\n")
	i := 0
	for i < len(lines) {
		line := lines[i]

		// Fenced code block
		if m := reFenceOpen.FindStringSubmatch(line); m != nil {
			lang := m[1]
			i++
			var codeLines []string
			for i < len(lines) && lines[i] != "```" {
				codeLines = append(codeLines, lines[i])
				i++
			}
			if i < len(lines) {
				i++ // skip closing ```
			}
			code := html.EscapeString(strings.Join(codeLines, "\n"))
			if lang != "" {
				sb.WriteString(fmt.Sprintf("<pre><code class=\"language-%s\">%s</code></pre>\n", html.EscapeString(lang), code))
			} else {
				sb.WriteString("<pre><code>" + code + "</code></pre>\n")
			}
			continue
		}

		// Horizontal rule
		if reHR.MatchString(line) {
			sb.WriteString(hrLine + "\n")
			i++
			continue
		}

		// Markdown table: row line followed by separator line
		if reTableRow.MatchString(line) && i+1 < len(lines) && reTableSep.MatchString(lines[i+1]) {
			var rows [][]string
			rows = append(rows, splitTableRow(line))
			i += 2 // skip header + separator
			for i < len(lines) && reTableRow.MatchString(lines[i]) {
				rows = append(rows, splitTableRow(lines[i]))
				i++
			}
			sb.WriteString(renderTable(rows))
			sb.WriteByte('\n')
			continue
		}

		// Blockquote: one or more consecutive > lines
		if reBlockquote.MatchString(line) {
			var quoteLines []string
			for i < len(lines) && reBlockquote.MatchString(lines[i]) {
				m := reBlockquote.FindStringSubmatch(lines[i])
				quoteLines = append(quoteLines, processInline(m[1]))
				i++
			}
			content := strings.Join(quoteLines, "\n")
			openTag := "<blockquote>"
			if len(content) > 300 || len(quoteLines) > 5 {
				openTag = "<blockquote expandable>"
			}
			sb.WriteString(openTag + content + "</blockquote>\n")
			continue
		}

		// Footnote definition: [^id]: text
		if m := reFootnoteDef.FindStringSubmatch(line); m != nil {
			label := toSuper(m[1])
			sb.WriteString("<i>" + label + " " + processInline(m[2]) + "</i>\n")
			i++
			continue
		}

		sb.WriteString(processLine(line))
		sb.WriteByte('\n')
		i++
	}
	return strings.TrimSpace(sb.String())
}

func processLine(line string) string {
	if m := reHeader.FindStringSubmatch(line); m != nil {
		level := len(m[1])
		inner := processInline(m[2])
		switch level {
		case 1:
			return "<b><u>" + inner + "</u></b>"
		case 2:
			return "<b>" + inner + "</b>"
		default:
			return "<b><i>" + inner + "</i></b>"
		}
	}
	if m := reBulletItem.FindStringSubmatch(line); m != nil {
		indent := strings.Repeat("  ", len(m[1])/2)
		rest := m[2]
		prefix := "• "
		if reCheckboxDone.MatchString(rest) {
			rest = reCheckboxDone.ReplaceAllString(rest, "")
			prefix = "☑ "
		} else if reCheckbox.MatchString(rest) {
			rest = reCheckbox.ReplaceAllString(rest, "")
			prefix = "☐ "
		}
		return indent + prefix + processInline(rest)
	}
	if m := reNumItem.FindStringSubmatch(line); m != nil {
		return html.EscapeString(m[1]) + " " + processInline(m[2])
	}
	return processInline(line)
}

func processInline(text string) string {
	var spans []string
	push := func(s string) string {
		spans = append(spans, s)
		return fmt.Sprintf("\x00%d\x00", len(spans)-1)
	}

	// 1) Backslash escapes → placeholder with the literal char.
	//    Done first so \*, \_, etc. are not consumed by later regexes.
	text = reEscape.ReplaceAllStringFunc(text, func(m string) string {
		return push(html.EscapeString(m[1:]))
	})

	// 2) Inline code → placeholder (content is HTML-escaped).
	text = reInlineCode.ReplaceAllStringFunc(text, func(m string) string {
		inner := html.EscapeString(m[1 : len(m)-1])
		return push("<code>" + inner + "</code>")
	})

	// 3) HTML-escape the remaining raw text without touching placeholders.
	text = escapeNonPlaceholders(text)

	// 4) Inline formatting. Order: links → spoiler → strike → bold → italic.
	text = reLink.ReplaceAllString(text, `<a href="$2">$1</a>`)
	text = reSpoiler.ReplaceAllStringFunc(text, func(m string) string {
		return "<tg-spoiler>" + m[2:len(m)-2] + "</tg-spoiler>"
	})
	text = reStrike.ReplaceAllStringFunc(text, func(m string) string {
		return "<s>" + m[2:len(m)-2] + "</s>"
	})
	text = reBoldStar.ReplaceAllStringFunc(text, func(m string) string {
		return "<b>" + m[2:len(m)-2] + "</b>"
	})
	text = reBoldUnder.ReplaceAllStringFunc(text, func(m string) string {
		return "<b>" + m[2:len(m)-2] + "</b>"
	})
	text = reItalicStar.ReplaceAllStringFunc(text, func(m string) string {
		return "<i>" + m[1:len(m)-1] + "</i>"
	})
	text = reItalicUnder.ReplaceAllStringFunc(text, func(m string) string {
		return "<i>" + m[1:len(m)-1] + "</i>"
	})

	// 5) Footnote references [^N] → superscript.
	text = reFootnoteRef.ReplaceAllStringFunc(text, func(m string) string {
		return toSuper(m[2 : len(m)-1])
	})

	// 6) Restore placeholders (code spans + escaped literals).
	text = rePlaceholder.ReplaceAllStringFunc(text, func(m string) string {
		var idx int
		fmt.Sscanf(m[1:len(m)-1], "%d", &idx)
		if idx < len(spans) {
			return spans[idx]
		}
		return m
	})

	return text
}

func escapeNonPlaceholders(text string) string {
	locs := rePlaceholder.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return html.EscapeString(text)
	}
	var sb strings.Builder
	last := 0
	for _, loc := range locs {
		sb.WriteString(html.EscapeString(text[last:loc[0]]))
		sb.WriteString(text[loc[0]:loc[1]])
		last = loc[1]
	}
	sb.WriteString(html.EscapeString(text[last:]))
	return sb.String()
}

func toSuper(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if sup, ok := supDigits[r]; ok {
			sb.WriteRune(sup)
		} else {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func splitTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.Trim(line, "|")
	cells := strings.Split(line, "|")
	for i, c := range cells {
		cells[i] = strings.TrimSpace(c)
	}
	return cells
}

// renderTable emits a Telegram <pre> block with space-padded columns and
// box-drawing separators. Telegram renders <pre> in a monospace font so
// single-character widths align correctly.
func renderTable(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	ncols := 0
	for _, r := range rows {
		if len(r) > ncols {
			ncols = len(r)
		}
	}
	widths := make([]int, ncols)
	for _, r := range rows {
		for i, c := range r {
			if w := runeCount(c); w > widths[i] {
				widths[i] = w
			}
		}
	}
	var sb strings.Builder
	sb.WriteString("<pre>")
	for rowIdx, r := range rows {
		for i := 0; i < ncols; i++ {
			var cell string
			if i < len(r) {
				cell = r[i]
			}
			if i > 0 {
				sb.WriteString(" │ ")
			}
			sb.WriteString(html.EscapeString(cell))
			sb.WriteString(strings.Repeat(" ", widths[i]-runeCount(cell)))
		}
		sb.WriteByte('\n')
		if rowIdx == 0 {
			for i := 0; i < ncols; i++ {
				if i > 0 {
					sb.WriteString("─┼─")
				}
				sb.WriteString(strings.Repeat("─", widths[i]))
			}
			sb.WriteByte('\n')
		}
	}
	return strings.TrimRight(sb.String(), "\n") + "</pre>"
}

func runeCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
