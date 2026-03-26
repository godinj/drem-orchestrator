package tui

import (
	"time"

	"github.com/google/uuid"
)

// EventMsg wraps an Event as a tea.Msg.
type EventMsg Event

// periodicRefreshMsg triggers a periodic data refresh from the DB.
type periodicRefreshMsg struct{}

// periodicRefreshInterval is how often the TUI re-reads agent data from the
// DB, so that continuously-updated fields like context_used_pct are visible
// without waiting for an orchestrator event.
const periodicRefreshInterval = 2 * time.Second

// logCapturedMsg carries captured tmux pane output.
type logCapturedMsg struct {
	forTaskID uuid.UUID
	text      string
	err       error
}

// orchLogCapturedMsg carries orchestrator log file content.
type orchLogCapturedMsg struct {
	forTaskID uuid.UUID
	text      string
	err       error
}

// supervisorSpawnedMsg carries the result of spawning a supervisor session.
type supervisorSpawnedMsg struct {
	sessionName string
	err         error
}

// reviewerSpawnedMsg carries the result of spawning a reviewer session.
type reviewerSpawnedMsg struct {
	sessionName string
	err         error
}

// fixerSpawnedMsg carries the result of spawning a fixer session.
type fixerSpawnedMsg struct {
	sessionName string
	err         error
}

// feedbackAction tracks what action triggered the feedback dialog.
type feedbackAction int

const (
	feedbackNone                feedbackAction = iota
	feedbackAddComment                         // add comment to task
	feedbackTestReviewReject                   // reject test review with feedback
	feedbackClarificationAnswer                // answer a clarification question
	feedbackBugReportComment                   // add comment to bug report
)

// confirmAction tracks a pending gate action awaiting user confirmation.
type confirmAction int

const (
	confirmNone              confirmAction = iota
	confirmPlanApprove                     // approve plan (plan_review → test_writing)
	confirmPlanReject                      // reject plan (plan_review → planning)
	confirmTestReviewApprove               // approve test review (test_review → in_progress)
	confirmTestPass                        // pass testing (testing_ready → merging)
	confirmTestFail                        // fail testing (testing_ready → in_progress)
)

// bugReportActionMsg is sent after a bug report action completes.
type bugReportActionMsg struct {
	err error
}

// editorFinishedMsg is sent when the external editor closes after promotion.
type editorFinishedMsg struct {
	tempFile    string
	bugReportID uuid.UUID
	err         error
}
