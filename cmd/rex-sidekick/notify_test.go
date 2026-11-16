package main

import (
	"os"
	"strings"
	"testing"
)

func TestPromptHasText(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	rule := strings.Repeat("─", 40)
	for name, c := range map[string]struct {
		screen string
		want   bool
	}{
		"empty box with placeholder": {read("prompt_empty.vt"), false},
		"half-written message":       {read("prompt_typed.vt"), true},
		"no input box":               {"$ ls\r\nfoo bar\r\n", false},
		"second line has text":       {rule + "\n❯\u00a0\n  more text\n" + rule + "\n", true},
		"text in an older box only":  {rule + "\n❯ old\n" + rule + "\nlater output\n" + rule + "\n❯\u00a0\x1b[2mTry it\x1b[0m\n" + rule + "\n", false},
		"menu outside a box":         {"❯ No, exit\n  Yes\n", false},
	} {
		if got := promptHasText(c.screen); got != c.want {
			t.Errorf("%s: got %v, want %v", name, got, c.want)
		}
	}
}

func TestPRNoticeTargetFallsBackToDispatcher(t *testing.T) {
	alive := map[string]bool{"block:worker": true}
	orig := paneExists
	paneExists = func(_, block string) bool { return alive[block] }
	defer func() { paneExists = orig }()

	d := Dispatcher{Session: "session:s", Block: "block:dispatcher"}
	for name, c := range map[string]struct {
		e              Entry
		haveDispatcher bool
		want           string
	}{
		"worker pane still open":   {Entry{NotifyBlock: "block:worker"}, true, "block:worker"},
		"worker pane closed":       {Entry{NotifySession: "session:s", NotifyBlock: "block:gone"}, true, "block:dispatcher"},
		"closed, no dispatcher":    {Entry{NotifyBlock: "block:gone"}, false, "block:gone"},
		"registered by dispatcher": {Entry{NotifyBlock: "block:dispatcher"}, true, "block:dispatcher"},
		"nobody to tell":           {Entry{}, true, ""},
	} {
		if got := prNoticeTarget(c.e, d, c.haveDispatcher).NotifyBlock; got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}
