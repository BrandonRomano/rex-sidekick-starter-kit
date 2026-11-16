package main

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Agents write an inbox item's detail in Markdown. renderMarkdown draws the
// parts that read well in a narrow pane: headings, lists, quotes, rules, code
// blocks, and **bold**, *italic*, `code`, and [links](url) inside a line.
// Like a GitHub comment, a single line break stays a line break.

var (
	sCode    = lipgloss.NewStyle().Foreground(lipgloss.Color("#79c0ff"))
	sHeading = lipgloss.NewStyle().Bold(true).Foreground(colMerged)

	mdHeading = regexp.MustCompile(`^#{1,6}\s+(.*?)(?:\s+#+)?\s*$`)
	mdItem    = regexp.MustCompile(`^(\s*)([-*+]|\d{1,9}[.)])\s+(.*)$`)
	mdRule    = regexp.MustCompile(`^\s{0,3}([-*_])(\s*[-*_]){2,}\s*$`)
)

// renderMarkdown draws src as lines no wider than w.
func renderMarkdown(src string, w int) []string {
	w = max(w, 4)
	var out []string
	blank := func() {
		if len(out) > 0 && out[len(out)-1] != "" {
			out = append(out, "")
		}
	}
	lines := strings.Split(strings.ReplaceAll(src, "\t", "    "), "\n")
	itemIndent := -1 // the text column of the list item being continued
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \r")
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			itemIndent = -1
			blank()

		case strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~"):
			fence := trimmed[:3]
			blank()
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), fence); i++ {
				for _, l := range wrapHard(strings.TrimRight(lines[i], " \r"), w-2) {
					out = append(out, "  "+sCode.Render(l))
				}
			}
			out = append(out, "")
			itemIndent = -1

		case mdHeading.MatchString(trimmed) && !strings.HasPrefix(line, "    "):
			blank()
			text := mdHeading.FindStringSubmatch(trimmed)[1]
			out = append(out, wrapStyled(renderInline(text, sHeading), w)...)
			itemIndent = -1

		case mdRule.MatchString(line):
			blank()
			out = append(out, sDim.Render(strings.Repeat("─", w)), "")
			itemIndent = -1

		case mdItem.MatchString(line):
			m := mdItem.FindStringSubmatch(line)
			depth := min(len(m[1])/2, 3)
			marker := "•"
			if m[2][0] >= '0' && m[2][0] <= '9' {
				marker = m[2]
			}
			lead := strings.Repeat("  ", depth) + marker + " "
			pad := strings.Repeat(" ", lipgloss.Width(lead))
			for j, l := range wrapStyled(renderInline(m[3], lipgloss.NewStyle()), w-len(pad)) {
				if j == 0 {
					out = append(out, strings.Repeat("  ", depth)+sDim.Render(marker)+" "+l)
				} else {
					out = append(out, pad+l)
				}
			}
			itemIndent = len(pad)

		case itemIndent >= 0 && strings.HasPrefix(line, " "):
			// An indented line under a list item carries on that item.
			pad := strings.Repeat(" ", itemIndent)
			for _, l := range wrapStyled(renderInline(trimmed, lipgloss.NewStyle()), w-itemIndent) {
				out = append(out, pad+l)
			}

		case strings.HasPrefix(trimmed, ">"):
			text := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
			for _, l := range wrapStyled(renderInline(text, sDim.Italic(true)), w-2) {
				out = append(out, sDim.Render("│")+" "+l)
			}
			itemIndent = -1

		default:
			out = append(out, wrapStyled(renderInline(trimmed, lipgloss.NewStyle()), w)...)
			itemIndent = -1
		}
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// renderInline styles the inline Markdown in s. Each word is styled on its
// own, so the result can be wrapped at any space without a style running
// into the next line.
func renderInline(s string, base lipgloss.Style) string {
	var b strings.Builder
	emit := func(text string, style lipgloss.Style) {
		for i, word := range strings.Split(text, " ") {
			if i > 0 {
				b.WriteString(" ")
			}
			if word != "" {
				b.WriteString(style.Render(word))
			}
		}
	}
	plain := 0 // start of the text not yet written
	flush := func(end int) { emit(s[plain:end], base) }
	for i := 0; i < len(s); i++ {
		rest := s[i:]
		switch {
		case rest[0] == '\\' && len(rest) > 1 && strings.ContainsRune("\\`*_[]()#>-", rune(rest[1])):
			flush(i)
			emit(rest[1:2], base)
			i++
			plain = i + 1

		case rest[0] == '`':
			n := strings.Count(rest, "`") - strings.Count(strings.TrimLeft(rest, "`"), "`")
			tick := strings.Repeat("`", n)
			end := strings.Index(rest[n:], tick)
			if end < 0 {
				i += n - 1
				continue
			}
			flush(i)
			emit(strings.TrimSpace(rest[n:n+end]), sCode)
			i += n + end + n - 1
			plain = i + 1

		case strings.HasPrefix(rest, "**") || strings.HasPrefix(rest, "__"):
			mark := rest[:2]
			end := strings.Index(rest[2:], mark)
			if end <= 0 || rest[2] == ' ' || (mark == "__" && !wordEdge(s, i)) {
				i++
				continue
			}
			flush(i)
			b.WriteString(renderInline(rest[2:2+end], base.Bold(true)))
			i += 2 + end + 1
			plain = i + 1

		case rest[0] == '*' || rest[0] == '_':
			// Single * or _ is italic only when it hugs a word, so "a * b"
			// and snake_case stay as they are.
			end := strings.IndexByte(rest[1:], rest[0])
			if end <= 0 || rest[1] == ' ' || rest[end] == ' ' || (rest[0] == '_' && !wordEdge(s, i)) {
				continue
			}
			flush(i)
			b.WriteString(renderInline(rest[1:1+end], base.Italic(true)))
			i += end + 1
			plain = i + 1

		case rest[0] == '[':
			close := strings.Index(rest, "](")
			end := strings.IndexByte(rest, ')')
			if close < 0 || end < close {
				continue
			}
			text, url := rest[1:close], rest[close+2:end]
			flush(i)
			for j, word := range strings.Split(renderInline(text, base.Underline(true)), " ") {
				if j > 0 {
					b.WriteString(" ")
				}
				b.WriteString(link(url, word))
			}
			i += end
			plain = i + 1
		}
	}
	flush(len(s))
	return b.String()
}

// wordEdge reports whether position i in s starts or ends a word, so an
// underscore there can be emphasis rather than part of a name.
func wordEdge(s string, i int) bool {
	return i == 0 || s[i-1] == ' ' || strings.ContainsRune("([\"'", rune(s[i-1]))
}

// wrapStyled wraps styled text at its spaces to lines no wider than w,
// breaking a word only when it won't fit on a line of its own.
func wrapStyled(s string, w int) []string {
	w = max(w, 1)
	var lines []string
	line, used := "", 0
	for _, word := range strings.Split(s, " ") {
		ww := ansi.StringWidth(word)
		if ww == 0 {
			continue
		}
		if line != "" && used+1+ww > w {
			lines = append(lines, line)
			line, used = "", 0
		}
		if ww > w {
			parts := strings.Split(ansi.Hardwrap(word, w, false), "\n")
			lines = append(lines, parts[:len(parts)-1]...)
			word = parts[len(parts)-1]
			ww = ansi.StringWidth(word)
		}
		if line != "" {
			line += " "
			used++
		}
		line += word
		used += ww
	}
	if line != "" || len(lines) == 0 {
		lines = append(lines, line)
	}
	return lines
}
