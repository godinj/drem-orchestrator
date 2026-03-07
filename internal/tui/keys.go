package tui

import "github.com/charmbracelet/bubbles/key"

// keyMap defines all key bindings for the TUI dashboard.
type keyMap struct {
	Up       key.Binding
	Down     key.Binding
	Tab      key.Binding
	Enter    key.Binding
	Approve  key.Binding // a - approve plan / pass test
	Reject   key.Binding // r - reject plan / fail test
	TestPass key.Binding // t - pass test
	TestFail key.Binding // f - fail test
	Jump     key.Binding // g - jump to agent tmux window
	New      key.Binding // n - new task
	Pause    key.Binding // p - pause/resume
	Retry    key.Binding // R - retry failed
	Log           key.Binding // l - view agent log
	Comment       key.Binding // c - add comment
	DeleteComment key.Binding // d - delete last comment
	Supervisor    key.Binding // S - supervisor evaluation
	Archive       key.Binding // A - toggle archived agents
	Filter        key.Binding // F - toggle task filter
	Reap          key.Binding // C - clean up dead tmux sessions
	Quit          key.Binding // q
	Esc           key.Binding
}

// defaultKeyMap returns the default key bindings.
func defaultKeyMap() keyMap {
	return keyMap{
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("k/up", "move up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("j/down", "move down"),
		),
		Tab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "switch panel"),
		),
		Enter: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "select"),
		),
		Approve: key.NewBinding(
			key.WithKeys("a"),
			key.WithHelp("a", "approve"),
		),
		Reject: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "reject"),
		),
		TestPass: key.NewBinding(
			key.WithKeys("t"),
			key.WithHelp("t", "pass test"),
		),
		TestFail: key.NewBinding(
			key.WithKeys("f"),
			key.WithHelp("f", "fail test"),
		),
		Jump: key.NewBinding(
			key.WithKeys("g"),
			key.WithHelp("g", "jump to agent"),
		),
		New: key.NewBinding(
			key.WithKeys("n"),
			key.WithHelp("n", "new task"),
		),
		Pause: key.NewBinding(
			key.WithKeys("p"),
			key.WithHelp("p", "pause/resume"),
		),
		Retry: key.NewBinding(
			key.WithKeys("R"),
			key.WithHelp("R", "retry failed"),
		),
		Log: key.NewBinding(
			key.WithKeys("l"),
			key.WithHelp("l", "view log"),
		),
		Comment: key.NewBinding(
			key.WithKeys("c"),
			key.WithHelp("c", "add comment"),
		),
		DeleteComment: key.NewBinding(
			key.WithKeys("d"),
			key.WithHelp("d", "delete comment"),
		),
		Supervisor: key.NewBinding(
			key.WithKeys("S"),
			key.WithHelp("S", "supervisor"),
		),
		Archive: key.NewBinding(
			key.WithKeys("A"),
			key.WithHelp("A", "archive"),
		),
		Filter: key.NewBinding(
			key.WithKeys("F"),
			key.WithHelp("F", "filter"),
		),
		Reap: key.NewBinding(
			key.WithKeys("C"),
			key.WithHelp("C", "clean sessions"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q"),
			key.WithHelp("q", "quit"),
		),
		Esc: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "back"),
		),
	}
}
