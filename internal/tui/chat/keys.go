package chat

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Quit       key.Binding
	Send       key.Binding
	NextTab    key.Binding
	PrevTab    key.Binding
	Tab1       key.Binding
	Tab2       key.Binding
	Tab3       key.Binding
	Tab4       key.Binding
	Quick1     key.Binding
	Quick2     key.Binding
	Quick3     key.Binding
	ScrollUp   key.Binding
	ScrollDown key.Binding
	LineUp     key.Binding
	LineDown   key.Binding
}

var keys = keyMap{
	Quit: key.NewBinding(
		key.WithKeys("ctrl+c"),
		key.WithHelp("ctrl+c", "quit"),
	),
	Send: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "send"),
	),
	NextTab: key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "next agent"),
	),
	PrevTab: key.NewBinding(
		key.WithKeys("shift+tab"),
		key.WithHelp("shift+tab", "prev agent"),
	),
	Tab1: key.NewBinding(key.WithKeys("ctrl+1", "alt+1")),
	Tab2: key.NewBinding(key.WithKeys("ctrl+2", "alt+2")),
	Tab3: key.NewBinding(key.WithKeys("ctrl+3", "alt+3")),
	Tab4: key.NewBinding(key.WithKeys("ctrl+4", "alt+4")),
	Quick1: key.NewBinding(
		key.WithKeys("f1"),
		key.WithHelp("F1", "status"),
	),
	Quick2: key.NewBinding(
		key.WithKeys("f2"),
		key.WithHelp("F2", "check"),
	),
	Quick3: key.NewBinding(
		key.WithKeys("f3"),
		key.WithHelp("F3", "yes"),
	),
	ScrollUp: key.NewBinding(
		key.WithKeys("pgup"),
		key.WithHelp("pgup", "scroll up"),
	),
	ScrollDown: key.NewBinding(
		key.WithKeys("pgdown"),
		key.WithHelp("pgdown", "scroll down"),
	),
	LineUp: key.NewBinding(
		key.WithKeys("up"),
		key.WithHelp("↑", "scroll up"),
	),
	LineDown: key.NewBinding(
		key.WithKeys("down"),
		key.WithHelp("↓", "scroll down"),
	),
}
