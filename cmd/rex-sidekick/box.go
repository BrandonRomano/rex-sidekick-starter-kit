package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// boxTop is a box's top border with title set into it, like "┌─ Agents ──┐".
func boxTop(title string, w int) string {
	title = truncate(title, max(1, w-6))
	fill := max(1, w-5-lipgloss.Width(title))
	return sDim.Render("┌─ ") + title + sDim.Render(" "+strings.Repeat("─", fill)+"┐")
}

// boxSides puts each line of block between a box's side borders.
func boxSides(block string, w int) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimSuffix(block, "\n"), "\n") {
		if block == "" {
			break
		}
		pad := max(0, w-4-lipgloss.Width(l))
		b.WriteString(sDim.Render("│") + " " + l + strings.Repeat(" ", pad) + " " + sDim.Render("│") + "\n")
	}
	return b.String()
}

// boxBottom is a box's bottom border.
func boxBottom(w int) string {
	return sDim.Render("└" + strings.Repeat("─", max(0, w-2)) + "┘")
}
