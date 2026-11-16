package main

import (
	"os"
	"testing"
)

// TestMain keeps tests away from the real Rex panes they might be run in.
// The sidekick resizes its own pane and splits panes beside it, so it reads
// REX_BLOCK. It also hides the claude CLI's model catalog, so the review
// model picker lists the fallback aliases unless a test points it elsewhere.
func TestMain(m *testing.M) {
	os.Unsetenv("REX_BLOCK")
	dir, err := os.MkdirTemp("", "claude-config")
	if err != nil {
		panic(err)
	}
	os.Setenv("CLAUDE_CONFIG_DIR", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
