package main

import (
	"cmp"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// The user edits one of their own tasks as a file: the task's text as a
// Markdown title on the first line, then a blank line, then its details in
// Markdown.

func todoFile(t Todo) string {
	if t.Detail == "" {
		return "# " + t.Text + "\n"
	}
	return "# " + t.Text + "\n\n" + t.Detail + "\n"
}

// parseTodoFile reads a task back from its file. The "#" before the text is
// optional, so deleting it changes nothing. Text comes back empty when the
// first line was cleared.
func parseTodoFile(s string) (text, detail string) {
	text, detail, _ = strings.Cut(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	text = strings.TrimSpace(text)
	if rest, ok := strings.CutPrefix(text, "#"); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
		text = strings.TrimSpace(rest)
	}
	return text, strings.TrimRight(strings.TrimLeft(detail, "\n"), " \t\n")
}

// editor is the command line that edits a file: $VISUAL, then $EDITOR,
// then vi.
func editor() string {
	return cmp.Or(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi")
}

// editTodo opens a pane under the sidekick that edits the selected task in
// the user's editor. The pane saves the task when the editor exits, and the
// tab picks the change up from the store.
func (m *model) editTodo(t Todo) {
	self, err := os.Executable()
	if err != nil {
		m.setError(err.Error())
		return
	}
	args := []string{"-s", m.st.Session, "--split=below", "--label", "Edit: " + truncate(t.Text, 40)}
	if b := os.Getenv("REX_BLOCK"); b != "" {
		args = append(args, "-b", b)
	}
	args = append(args, "--", self, "edit-pane", "--session", m.st.Session, t.ID)
	if out, err := rexSplit(args...); err != nil {
		m.setError("rex split: " + strings.TrimSpace(string(out)))
		return
	}
	m.note("Editing it in " + strings.Fields(editor())[0])
}

// cmdEditPane runs the user's editor on a task in the pane the sidekick
// opened for it, then saves what they wrote. The pane closes with it.
func cmdEditPane(args []string) error {
	fs, session := newFlags("edit-pane")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("edit-pane needs a task ID")
	}
	id := fs.Arg(0)
	st, err := OpenStore(*session)
	if err != nil {
		return err
	}
	ts, err := st.Todos()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(ts, func(t Todo) bool { return t.ID == id })
	if i < 0 {
		return pause("That task is gone, so there's nothing to edit.")
	}
	before := todoFile(ts[i])
	f, err := os.CreateTemp("", "rex-sidekick-"+id+"-*.md")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(before)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}

	// Through the shell, so an editor with flags, like "code -w", works.
	cmd := exec.Command(cmp.Or(os.Getenv("SHELL"), "/bin/sh"), "-c", editor()+` "$1"`, "edit", f.Name())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		// Like :cq in vim, a failed exit throws the edit away.
		return pause(fmt.Sprintf("The editor exited with %v, so the task wasn't changed.", err))
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		return err
	}
	if string(b) == before {
		return nil
	}
	text, detail := parseTodoFile(string(b))
	err = st.UpdateTodo(id, func(t *Todo, _ []Todo) error {
		if text != "" {
			t.Text = text
		}
		t.Detail = detail
		return nil
	})
	if err != nil {
		return pause("Couldn't save the task: " + err.Error())
	}
	return nil
}

// pause shows msg until the user presses enter, so it isn't lost when the
// pane closes.
func pause(msg string) error {
	fmt.Printf("%s\n\x1b[2mPress enter to close this pane.\x1b[0m", msg)
	_, _ = fmt.Scanln()
	return nil
}
