package chat

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Quit             key.Binding
	Send             key.Binding
	NextTab          key.Binding
	PrevTab          key.Binding
	Tab1             key.Binding
	Tab2             key.Binding
	Tab3             key.Binding
	Tab4             key.Binding
	Quick1           key.Binding
	Quick2           key.Binding
	Quick3           key.Binding
	OpenInbox        key.Binding
	OpenControl      key.Binding
	OpenModel        key.Binding
	Refresh          key.Binding
	Archive          key.Binding
	Ignore           key.Binding
	StopPersona      key.Binding
	StartPersona     key.Binding
	RecreatePersona  key.Binding
	SelectAllControl key.Binding
	Cancel           key.Binding
	ScrollUp         key.Binding
	ScrollDown       key.Binding
	LineUp           key.Binding
	LineDown         key.Binding
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
	OpenInbox: key.NewBinding(
		key.WithKeys("ctrl+s", "f6"),
		key.WithHelp("ctrl+s/F6", "inbox"),
	),
	OpenControl: key.NewBinding(
		key.WithKeys("ctrl+d", "f7"),
		key.WithHelp("ctrl+d/F7", "control"),
	),
	OpenModel: key.NewBinding(
		key.WithKeys("f8"),
		key.WithHelp("F8", "model"),
	),
	Refresh: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "refresh"),
	),
	Archive: key.NewBinding(
		key.WithKeys("a"),
		key.WithHelp("a", "archive"),
	),
	Ignore: key.NewBinding(
		key.WithKeys("x"),
		key.WithHelp("x", "ignore"),
	),
	StopPersona: key.NewBinding(
		key.WithKeys("s"),
		key.WithHelp("s", "stop"),
	),
	StartPersona: key.NewBinding(
		key.WithKeys("S"),
		key.WithHelp("S", "start"),
	),
	RecreatePersona: key.NewBinding(
		key.WithKeys("R"),
		key.WithHelp("R", "recreate"),
	),
	SelectAllControl: key.NewBinding(
		key.WithKeys("A"),
		key.WithHelp("A", "all"),
	),
	Cancel: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "back"),
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
