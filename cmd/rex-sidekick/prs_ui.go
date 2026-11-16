package main

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// The groups on the PRs tab, in the order they show.
const (
	groupReview = iota
	groupMine
	groupOthers
)

var groupNames = [...]string{"Needs review", "Mine", "Others"}

// prItem is one row of the PRs tab.
type prItem struct {
	Entry
	group int
}

// prItems lists the PRs tab: review requests grouped by author, then our PRs,
// then everyone else's. Merged and closed PRs go last in their group. Until
// we know our login, every watched PR counts as someone else's.
func (m *model) prItems() []prItem {
	var out []prItem
	for _, e := range m.reviews {
		out = append(out, prItem{e, groupReview})
	}
	all := append(slices.Clone(m.entries), m.closed...)
	for _, g := range []int{groupMine, groupOthers} {
		for _, e := range all {
			if m.mine(e) == (g == groupMine) {
				out = append(out, prItem{e, g})
			}
		}
	}
	return out
}

// selectedPR returns the PR under the PRs tab's cursor, whichever tab is open.
func (m *model) selectedPR() (prItem, bool) {
	items, i := m.prItems(), m.cursors[tabPRs]
	if i >= 0 && i < len(items) {
		return items[i], true
	}
	return prItem{}, false
}

// keepSelection runs change, then puts the PRs tab's cursor back on the PR it
// was on, if that PR is still listed.
func (m *model) keepSelection(change func()) {
	sel, ok := m.selectedPR()
	change()
	if !ok {
		return
	}
	i := slices.IndexFunc(m.prItems(), func(p prItem) bool { return p.URL == sel.URL && p.group == sel.group })
	if i >= 0 {
		m.cursors[tabPRs] = i
	}
}

// removePR stops watching a PR, or clears a merged or closed one off the tab.
func (m *model) removePR(e Entry) {
	if m.isClosed(e.URL) {
		m.closed = slices.DeleteFunc(m.closed, func(c Entry) bool { return c.URL == e.URL })
		return
	}
	if _, err := m.st.Remove(e.URL); err != nil {
		m.setError(err.Error())
	}
	m.reload()
}

// prHints are the footer's keys for the selected PR's group.
func (m *model) prHints() []string {
	p, ok := m.selectedPR()
	switch {
	case !ok:
		return []string{"r refresh"}
	case p.group == groupMine:
		hints := []string{"m merge", "x remove", "r refresh"}
		if !m.isClosed(p.URL) {
			hints = slices.Insert(hints, 0, "c self-review")
		}
		return hints
	case p.group == groupReview:
		return []string{"c Claude review", "u unassign", "r refresh"}
	}
	return []string{"x remove", "r refresh"}
}

// prBlocks draws the PRs tab, one block per PR, with each group in a box
// titled with its name. Empty groups don't show.
func (m *model) prBlocks(cur, w int) []string {
	items := m.prItems()
	inner := max(10, w-4)
	blocks := make([]string, len(items))
	for i, p := range items {
		first := i == 0 || items[i-1].group != p.group
		last := i+1 == len(items) || items[i+1].group != p.group
		block := ""
		if p.group == groupReview && (first || p.Author != items[i-1].Author) {
			n := 0
			for _, r := range m.reviews {
				if r.Author == p.Author {
					n++
				}
			}
			author := cmp.Or(p.Author, "unknown author")
			block = sTopic.Render(truncate(fmt.Sprintf("%s (%d)", author, n), inner)) + "\n"
		}
		if !first && m.isClosed(p.URL) && !m.isClosed(items[i-1].URL) {
			// A rule between open PRs and the merged or closed ones below.
			block = sDim.Render(strings.Repeat("─", inner)) + "\n\n"
		}
		block += m.renderEntry(p.Entry, p.group, i == cur, inner)
		if in := m.reviewInput; in != nil && p.group == groupReview && in.e.URL == p.URL {
			// Put the box above the blank line that ends the entry.
			block = strings.TrimSuffix(block, "\n") + in.render(inner) + "\n"
		}
		if mp := m.modelPicker; mp != nil && p.group != groupOthers && mp.e.URL == p.URL {
			block = strings.TrimSuffix(block, "\n") + mp.render(inner) + "\n"
		}
		if last {
			// No blank line above the bottom border.
			block = strings.TrimSuffix(block, "\n")
		}
		block = boxSides(block, w)
		if first {
			title := groupNames[p.group]
			if p.group == groupMine {
				n := 0
				for _, q := range items {
					if q.group == groupMine {
						n++
					}
				}
				title = fmt.Sprintf("My PRs (%d)", n)
			}
			top := boxTop(title, w) + "\n"
			if i > 0 {
				top = "\n" + top
			}
			block = top + block
		}
		if last {
			block += boxBottom(w) + "\n"
		}
		blocks[i] = block
	}
	return blocks
}
