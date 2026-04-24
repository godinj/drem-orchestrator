package chat

import "github.com/charmbracelet/lipgloss"

var (
	// Colors — dark theme matching the PWA.
	colorPrimary     = lipgloss.Color("#7C8CFF")
	colorDim         = lipgloss.Color("#666666")
	colorBright      = lipgloss.Color("#EEEEEE")
	colorGreen       = lipgloss.Color("#4ADE80")
	colorYellow      = lipgloss.Color("#FACC15")
	colorRed         = lipgloss.Color("#F87171")
	colorBg          = lipgloss.Color("#1E1E2E")
	colorBorder      = lipgloss.Color("#444444")
	colorAgentBubble = lipgloss.Color("#2A2A3E")
	colorOperBubble  = lipgloss.Color("#1E3A5F")

	// Tab bar styles.
	styleTabActive = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorBright).
			Background(colorPrimary).
			Padding(0, 1)

	styleTabInactive = lipgloss.NewStyle().
				Foreground(colorDim).
				Padding(0, 1)

	styleTabBadge = lipgloss.NewStyle().
			Foreground(colorRed).
			Bold(true)

	// Message styles.
	styleAgentName = lipgloss.NewStyle().
			Foreground(colorPrimary).
			Bold(true)

	styleOperatorName = lipgloss.NewStyle().
				Foreground(colorGreen).
				Bold(true)

	styleTimestamp = lipgloss.NewStyle().
			Foreground(colorDim)

	styleAgentBody = lipgloss.NewStyle().
			Foreground(colorBright)

	styleOperatorBody = lipgloss.NewStyle().
				Foreground(colorBright)

	// Quick action buttons.
	styleQuickAction = lipgloss.NewStyle().
				Foreground(colorBright).
				Background(lipgloss.Color("#333344")).
				Padding(0, 1)

	// Status indicator.
	styleConnected = lipgloss.NewStyle().
			Foreground(colorGreen).
			Bold(true)

	styleDisconnected = lipgloss.NewStyle().
				Foreground(colorRed).
				Bold(true)

	styleConnecting = lipgloss.NewStyle().
			Foreground(colorYellow)

	// Header/border.
	styleHeader = lipgloss.NewStyle().
			Foreground(colorBright).
			Bold(true)

	styleBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder)

	styleHRule = lipgloss.NewStyle().
			Foreground(colorBorder)

	// Input prompt.
	stylePrompt = lipgloss.NewStyle().
			Foreground(colorPrimary).
			Bold(true)
)
