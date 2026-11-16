package main

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestInbox(t *testing.T) {
	t.Setenv("REX_SIDEKICK_STATE_DIR", t.TempDir())
	b, err := openInbox("block:abc")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if b.Listening(now) {
		t.Fatal("nobody has waited yet, so nobody is listening")
	}

	type result struct {
		lines []string
		err   error
	}
	got := make(chan result)
	go func() {
		lines, err := b.Wait(10 * time.Millisecond)
		got <- result{lines, err}
	}()
	for !b.Listening(time.Now()) {
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := b.Wait(time.Millisecond); !errors.Is(err, errAlreadyWaiting) {
		t.Errorf("second wait: got %v, want errAlreadyWaiting", err)
	}

	b.Push("[sidekick] one")
	b.Push("[sidekick] two")
	r := <-got
	if r.err != nil {
		t.Fatal(r.err)
	}
	// Both notices may land in one read, or the first alone.
	if want := []string{"[sidekick] one", "[sidekick] two"}; !reflect.DeepEqual(r.lines, want) && !reflect.DeepEqual(r.lines, want[:1]) {
		t.Errorf("wait returned %q", r.lines)
	}
	rest, _ := b.Take()
	if len(r.lines)+len(rest) != 2 {
		t.Errorf("lost a notice: got %q then %q", r.lines, rest)
	}

	// Right after a wait exits, the agent is probably about to wait again.
	b.Push("[sidekick] three")
	if !b.Listening(time.Now()) || b.Stale(time.Now()) {
		t.Error("a wait that just ended should still count as listening")
	}

	// Long after, nobody is coming back for it.
	old := time.Now().Add(-2 * waitGrace)
	if err := os.Chtimes(b.path("wait"), old, old); err != nil {
		t.Fatal(err)
	}
	if b.Listening(time.Now()) || !b.Stale(time.Now()) {
		t.Error("an inbox nobody has waited on for a while should be stale")
	}
}

func TestUnshiftPutsNoticesBackFirst(t *testing.T) {
	t.Setenv("REX_SIDEKICK_STATE_DIR", t.TempDir())
	b, err := openInbox("block:unshift")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Push("newer"); err != nil {
		t.Fatal(err)
	}
	if err := b.Unshift([]string{"first", "second"}); err != nil {
		t.Fatal(err)
	}
	got, err := b.Take()
	if err != nil || strings.Join(got, ",") != "first,second,newer" {
		t.Errorf("got %v, %v", got, err)
	}
}
