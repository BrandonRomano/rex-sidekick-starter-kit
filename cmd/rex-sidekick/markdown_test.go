package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRenderMarkdown(t *testing.T) {
	src := strings.Join([]string{
		"## PR ready for review",
		"Branch `ab/retry-loop` in **the worktree**, see [PR #12](https://github.com/acme/w/pull/12).",
		"Leave snake_case_names and 2 * 3 alone.",
		"",
		"- first item, long enough that it has to wrap around",
		"  and carries on here",
		"- second, with *emphasis*",
		"  1. nested",
		"",
		"> quoted",
		"",
		"```",
		"if x { y() }",
		"```",
	}, "\n")
	for _, w := range []int{24, 40, 70} {
		lines := renderMarkdown(src, w)
		var plain []string
		for _, l := range lines {
			if ansi.StringWidth(l) > w {
				t.Errorf("width %d: line is %d wide: %q", w, ansi.StringWidth(l), l)
			}
			plain = append(plain, ansi.Strip(l))
		}
		text := strings.Join(plain, "\n")
		for _, bad := range []string{"##", "**", "`", "](", "> "} {
			if strings.Contains(text, bad) {
				t.Errorf("width %d: markup %q should be gone:\n%s", w, bad, text)
			}
		}
		for _, want := range []string{"PR ready for review", "ab/retry-loop", "PR #12", "snake_case_names", "2 * 3", "• first", "1. nested", "│ quoted", "  if x { y() }"} {
			if !strings.Contains(strings.Join(strings.Fields(text), " "), strings.Join(strings.Fields(want), " ")) {
				t.Errorf("width %d: %q missing:\n%s", w, want, text)
			}
		}
	}

	lines := renderMarkdown("- first item, long enough that it has to wrap", 20)
	if len(lines) < 2 || !strings.HasPrefix(ansi.Strip(lines[1]), "  ") {
		t.Errorf("a wrapped list item should hang under its text: %q", lines)
	}
	if got := renderMarkdown("one\ntwo", 40); len(got) != 2 {
		t.Errorf("a single line break should stay a line break, got %q", got)
	}
}
