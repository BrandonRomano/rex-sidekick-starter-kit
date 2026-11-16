package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// FormatNotice builds the one-line message typed into the agent's pane.
func FormatNotice(e Entry, title string, changes []string) string {
	if title == "" {
		title = e.Title
	}
	msg := fmt.Sprintf("[sidekick] %s#%d %q: %s. %s",
		e.Repo, e.Number, truncate(title, 60), strings.Join(changes, "; "), e.URL)
	// A newline would submit the prompt early, so keep it to one line.
	return strings.Join(strings.Fields(msg), " ")
}

// Notify tells the entry's Rex pane about msg. It goes in the pane's inbox,
// where an agent listening with `rex-sidekick wait` picks it up. If no agent
// is listening, it's typed into the pane, unless the user is in the middle of
// typing there. Then it waits in the inbox until the input box is empty.
func Notify(e Entry, msg string) error {
	if e.NotifyBlock == "" {
		return nil
	}
	b, err := openInbox(e.NotifyBlock)
	if err != nil {
		return typeInto(e, msg)
	}
	// Notices already waiting go first, so they arrive in order.
	if err := b.Push(msg); err != nil {
		return err
	}
	return flushStale(e)
}

// flushStale types out inbox notices that no agent is going to pick up, such
// as when an agent stopped running `wait`.
func flushStale(e Entry) error {
	if e.NotifyBlock == "" {
		return nil
	}
	b, err := openInbox(e.NotifyBlock)
	if err != nil || !b.Stale(time.Now()) {
		return err
	}
	lines, err := b.Take()
	if err != nil {
		return err
	}
	for i, l := range lines {
		if err := typeInto(e, l); err != nil {
			if perr := b.Unshift(lines[i:]); perr != nil {
				return perr
			}
			if errors.Is(err, errUserTyping) {
				return nil // try again on the next flush
			}
			return err
		}
	}
	return nil
}

var errUserTyping = errors.New("the user is typing in that pane")

// paneExists reports whether block is still a pane in session. A variable so
// tests can stand in for Rex.
var paneExists = func(session, block string) bool {
	if session == "" {
		session = os.Getenv("REX_SESSION")
	}
	return exec.Command("rex", "block", "inspect", "-s", session, block, "--json").Run() == nil
}

// prNoticeTarget picks the pane to tell about a PR change. When the pane that
// registered the PR has closed, the notice goes to the dispatcher, so a PR
// whose worker is gone still has someone to act on it.
func prNoticeTarget(e Entry, d Dispatcher, haveDispatcher bool) Entry {
	if e.NotifyBlock == "" || !haveDispatcher || d.Block == e.NotifyBlock {
		return e
	}
	if paneExists(e.NotifySession, e.NotifyBlock) {
		return e
	}
	e.NotifySession, e.NotifyBlock = d.Session, d.Block
	return e
}

// typeInto types msg into the entry's Rex pane and presses Return. If the
// user has text in Claude Code's input box there, it returns errUserTyping
// instead, since Return would send their half-written message.
func typeInto(e Entry, msg string) error {
	session := e.NotifySession
	if session == "" {
		session = os.Getenv("REX_SESSION")
	}
	if screen, err := exec.Command("rex", "capture", "-s", session, "-b", e.NotifyBlock, "--format", "vt").Output(); err == nil && promptHasText(string(screen)) {
		return errUserTyping
	}
	if out, err := exec.Command("rex", "send", "-s", session, "-b", e.NotifyBlock, msg).CombinedOutput(); err != nil {
		return fmt.Errorf("rex send: %s", strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("rex", "send-key", "-s", session, "-b", e.NotifyBlock, "enter").CombinedOutput(); err != nil {
		return fmt.Errorf("rex send-key: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// promptHasText reports whether Claude Code's input box on screen has text
// in it. screen is the pane in vt format. The box is the last pair of ─ rules
// with a line starting "❯" right under the top one. The placeholder Claude
// Code shows in an empty box is dim, so dim text doesn't count. A screen
// with no such box has no text to lose.
func promptHasText(screen string) bool {
	lines := strings.Split(screen, "\n")
	isRule := func(l string) bool { return strings.HasPrefix(strings.TrimSpace(stripANSI(l)), "───") }
	bottom := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if !isRule(lines[i]) {
			continue
		}
		if bottom < 0 {
			bottom = i
			continue
		}
		if i+1 < bottom && strings.HasPrefix(strings.TrimSpace(stripANSI(lines[i+1])), "❯") {
			for _, l := range lines[i+1 : bottom] {
				if brightText(l) != "" {
					return true
				}
			}
			return false
		}
		bottom = i
	}
	return false
}

// brightText returns the visible text in a vt line that isn't dim, leaving out
// the "❯" prompt and spaces.
func brightText(line string) string {
	var b strings.Builder
	dim := false
	for i := 0; i < len(line); {
		if line[i] != 0x1b || i+1 >= len(line) {
			r, size := utf8.DecodeRuneInString(line[i:])
			if !dim && r != '❯' && !unicode.IsSpace(r) && r != 0xa0 && !unicode.IsControl(r) {
				b.WriteRune(r)
			}
			i += size
			continue
		}
		switch line[i+1] {
		case '[': // CSI: parameters, then a final byte from @ to ~
			j := i + 2
			for j < len(line) && (line[j] < 0x40 || line[j] > 0x7e) {
				j++
			}
			if j < len(line) && line[j] == 'm' {
				dim = sgrDim(line[i+2:j], dim)
			}
			i = j + 1
		case ']': // OSC: ends with BEL or ESC backslash
			j := i + 2
			for j < len(line) && line[j] != 0x07 && !(line[j] == 0x1b && j+1 < len(line) && line[j+1] == '\\') {
				j++
			}
			if j < len(line) && line[j] == 0x1b {
				j++
			}
			i = j + 1
		default:
			i += 2
		}
	}
	return b.String()
}

// sgrDim applies the SGR parameters params to the dim state.
func sgrDim(params string, dim bool) bool {
	ps := strings.FieldsFunc(params, func(r rune) bool { return r == ';' || r == ':' })
	if len(ps) == 0 {
		return false
	}
	for i := 0; i < len(ps); i++ {
		switch ps[i] {
		case "0":
			dim = false
		case "2":
			dim = true
		case "22":
			dim = false
		case "38", "48", "58": // extended colors carry their own numbers
			if i+1 < len(ps) && ps[i+1] == "5" {
				i += 2
			} else if i+1 < len(ps) && ps[i+1] == "2" {
				i += 4
			}
		}
	}
	return dim
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case '[':
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
		case ']':
			for i < len(s) && s[i] != 0x07 && !(s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\') {
				i++
			}
			if i < len(s) && s[i] == 0x1b {
				i++
			}
		default:
			i++
		}
	}
	return b.String()
}

// sidebarCols is how wide the sidekick pane opens, in columns. It's the width
// the five tabs used to need before the PR tabs became one, kept because PR
// rows and TODO boxes read well at it.
const sidebarCols = 71

// fitStep moves the sidekick's split, with the sidekick on the left, one
// step toward sidebarCols wide. Rex only learns a pane's size from a window
// that shows it, so a step only acts on a size reported after since, the time
// of the last step's resize. It reports whether the width is right, and when
// it resized.
func fitStep(session, sidekick string, since time.Time) (done bool, resized time.Time) {
	var size struct {
		Columns int `json:"columns"`
		Desired []struct {
			UpdatedAt time.Time `json:"updated_at"`
		} `json:"desired"`
	}
	out, err := exec.Command("rex", "block", "call", "-s", session, "-b", sidekick, "size").Output()
	if err != nil || json.Unmarshal(out, &size) != nil || size.Columns <= 0 || len(size.Desired) == 0 {
		return false, time.Time{}
	}
	if d := size.Columns - sidebarCols; d >= -1 && d <= 1 {
		return true, time.Time{}
	}
	fresh := false
	for _, d := range size.Desired {
		fresh = fresh || d.UpdatedAt.After(since)
	}
	if !fresh {
		return false, time.Time{}
	}

	out, err = exec.Command("rex", "session", "inspect", session, "--json").Output()
	if err != nil {
		return false, time.Time{}
	}
	var view struct {
		Windows []struct {
			Layers []struct {
				Layout layoutNode `json:"layout"`
			} `json:"layers"`
		} `json:"windows"`
	}
	if json.Unmarshal(out, &view) != nil {
		return false, time.Time{}
	}
	var split *layoutNode
	for _, w := range view.Windows {
		for _, l := range w.Layers {
			if n := l.Layout.splitAfter(sidekick); n != nil {
				split = n
			}
		}
	}
	// Not beside another pane, so there's nothing to resize against.
	if split == nil || split.Split.Before.Rect[2] <= 0 {
		return true, time.Time{}
	}
	anchor := split.Split.After.firstBlock()
	if anchor == "" {
		return true, time.Time{}
	}

	// Rects are fractions of the window, so the sidekick's width in columns
	// tells us how many columns the whole split has.
	splitCols := float64(size.Columns) / split.Split.Before.Rect[2] * split.Rect[2]
	// The ratio is the sidekick's share, and +0.5 rounds up.
	ratio := (float64(sidebarCols) + 0.5) / splitCols
	if ratio < 0.05 || ratio > 0.6 {
		return true, time.Time{}
	}
	args, _ := json.Marshal(map[string]any{
		"direction":       "horizontal",
		"before_block_id": sidekick,
		"after_block_id":  anchor,
		"ratio":           ratio,
	})
	if exec.Command("rex", "api", "call", "-s", session, "session.resize_pane", string(args)).Run() != nil {
		return true, time.Time{}
	}
	return false, time.Now()
}

type layoutNode struct {
	Rect    [4]float64 `json:"rect"`
	BlockID string     `json:"block_id"`
	Split   *struct {
		Direction string     `json:"direction"`
		Before    layoutNode `json:"before"`
		After     layoutNode `json:"after"`
	} `json:"split"`
}

// firstBlock is the leftmost pane in n.
func (n *layoutNode) firstBlock() string {
	if n.Split == nil {
		return n.BlockID
	}
	return n.Split.Before.firstBlock()
}

// splitAfter finds the split whose left side is exactly the block id.
func (n *layoutNode) splitAfter(id string) *layoutNode {
	if n.Split == nil {
		return nil
	}
	if n.Split.Direction == "horizontal" && n.Split.Before.BlockID == id {
		return n
	}
	if r := n.Split.Before.splitAfter(id); r != nil {
		return r
	}
	return n.Split.After.splitAfter(id)
}

// OpenSidekick starts the sidekick in a pane to the left of block, without
// taking focus. With no block it opens a new background tab instead.
func OpenSidekick(session, block, interval string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	var args []string
	if block != "" {
		args = []string{"split", "-s", session, "-b", block, "--split=left", "--ratio", "30",
			"--label", "Sidekick", "--focus=false", "--shell=none"}
	} else {
		args = []string{"window", "new", "-s", session, "Sidekick", "--focus=false", "--shell=none"}
	}
	args = append(args, "--", self, "ui", "--session", session)
	if interval != "" {
		args = append(args, "--interval", interval)
	}
	out, err := exec.Command("rex", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("rex %s: %s", args[0], strings.TrimSpace(string(out)))
	}
	// The sidekick fits its own width once a window shows it.
	return nil
}
