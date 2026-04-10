package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/experiment"
)

// ExperimentView represents a read-only view of experiments.
// It follows the same pattern as BoardModel and AgentsModel.
type ExperimentView struct {
	width  int
	height int
	db     *gorm.DB
	exps   []experiment.Experiment
	err    error
}

// NewExperimentView creates a new experiment view.
func NewExperimentView(db *gorm.DB) ExperimentView {
	return ExperimentView{
		db: db,
	}
}

// Init returns a command to load experiments from the database.
func (v ExperimentView) Init() tea.Cmd {
	return func() tea.Msg {
		experiments, err := experiment.ListActiveExperiments(v.db)
		if err != nil {
			return experimentLoadedMsg{err: err}
		}
		return experimentLoadedMsg{experiments: experiments}
	}
}

// Update handles messages for the experiment view.
func (v ExperimentView) Update(msg tea.Msg) (ExperimentView, tea.Cmd) {
	switch msg := msg.(type) {
	case experimentLoadedMsg:
		v.exps = msg.experiments
		v.err = msg.err
		return v, nil
	}
	return v, nil
}

// View renders the experiment list.
func (v ExperimentView) View() string {
	if v.err != nil {
		return fmt.Sprintf("Error: %v", v.err)
	}

	if len(v.exps) == 0 {
		return subtitleStyle.Render("  No active experiments.")
	}

	// Create header row
	header := lipgloss.NewStyle().
		Bold(true).
		Render("ID (short)  | Status   | Created    | Variants | Winner")

	var lines []string
	lines = append(lines, header)

	for _, exp := range v.exps {
		// Calculate variant stats
		total := len(exp.Variants)
		passed := 0
		failed := 0
		winner := ""

		for _, variant := range exp.Variants {
			switch variant.Status {
			case experiment.VariantPassed:
				passed++
			case experiment.VariantFailed:
				failed++
			case experiment.VariantWinner:
				winner = variant.ProfileName
			}
		}

		// Format ID (shorten to 8 chars)
		id := exp.ID.String()
		if len(id) > 8 {
			id = id[:8]
		}

		// Format created date
		created := exp.CreatedAt.Format("2006-01-02")

		variants := fmt.Sprintf("%d/%d", passed+failed, total)

		// Find winner variant
		if winner == "" {
			for _, variant := range exp.Variants {
				if variant.IsDefault {
					winner = variant.ProfileName
				}
			}
		}

		// Create row
		row := fmt.Sprintf("%s | %-8s | %-10s | %-9s | %s",
			id,
			exp.Status,
			created,
			variants,
			winner)

		lines = append(lines, row)
	}

	// Add spacing
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// SetSize updates the dimensions of the experiment view.
func (v *ExperimentView) SetSize(width, height int) {
	v.width = width
	v.height = height
}

// experimentLoadedMsg is sent when experiments are loaded from the database.
type experimentLoadedMsg struct {
	experiments []experiment.Experiment
	err         error
}
