package dialog

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

func newJobsForTest(entries []JobEntry) *Jobs {
	s := styles.CharmtonePantera()
	com := &common.Common{Styles: &s}
	d := NewJobs(com)
	d.SetJobs(entries)
	return d
}

func testJobEntries() []JobEntry {
	return []JobEntry{
		{ID: "001", Command: "go test ./...", Description: "run tests", Running: true, StartedAt: 100},
		{ID: "002", Command: "go build ./...", Running: false, ExitCode: 0, StartedAt: 50, CompletedAt: 60},
		{ID: "003", Command: "make lint", Running: false, ExitCode: 2, StartedAt: 10, CompletedAt: 20},
	}
}

func TestJobs_EnterRequestsOutput(t *testing.T) {
	t.Parallel()

	d := newJobsForTest(testJobEntries())
	require.Nil(t, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown}))

	action := d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	request, ok := action.(ActionJobOutputRequest)
	require.True(t, ok)
	require.Equal(t, "002", request.ID)
}

func TestJobs_KillSelectedJob(t *testing.T) {
	t.Parallel()

	d := newJobsForTest(testJobEntries())
	action := d.HandleMsg(tea.KeyPressMsg{Code: 'x'})

	kill, ok := action.(ActionKillJob)
	require.True(t, ok)
	require.Equal(t, "001", kill.ID, "the first row is selected by default")
}

func TestJobs_EmptyListHasNoActions(t *testing.T) {
	t.Parallel()

	d := newJobsForTest(nil)
	require.Nil(t, d.HandleMsg(tea.KeyPressMsg{Code: 'x'}))
	require.Nil(t, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))
	require.Empty(t, d.SelectedJobID())
}

func TestJobs_SetJobsKeepsSelection(t *testing.T) {
	t.Parallel()

	d := newJobsForTest(testJobEntries())
	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown})
	require.Equal(t, "002", d.SelectedJobID())

	// The running job moved to the top of the refreshed list; the
	// selection must follow the job, not the row index.
	d.SetJobs([]JobEntry{
		{ID: "003", Command: "make lint", Running: true, StartedAt: 10},
		{ID: "002", Command: "go build ./...", Running: false, StartedAt: 50, CompletedAt: 60},
	})
	require.Equal(t, "002", d.SelectedJobID())
}

func TestJobs_OutputModeUnwindsOnEscape(t *testing.T) {
	t.Parallel()

	d := newJobsForTest(testJobEntries())
	d.ShowOutput(JobOutput{JobID: "001", Command: "go test ./...", Running: true, Output: "ok"})
	require.True(t, d.InOutput())

	require.Nil(t, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape}))
	require.False(t, d.InOutput(), "escape returns to the list")
	require.Equal(t, "001", d.SelectedJobID())

	require.IsType(t, ActionClose{}, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape}))
}

func TestJobs_OutputScrollStaysInRange(t *testing.T) {
	t.Parallel()

	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	d := newJobsForTest(nil)
	d.ShowOutput(JobOutput{JobID: "001", Output: strings.Join(lines, "\n")})
	d.height = 5

	// Following the tail until the first draw is harmless.
	d.scrollOutput(-3)
	require.Zero(t, d.outputAt)

	d.scrollOutput(100)
	require.Equal(t, 15, d.outputAt, "the window stops at the newest lines")
	require.True(t, d.outputEnd)

	d.scrollOutput(-2)
	require.Equal(t, 13, d.outputAt)
	require.False(t, d.outputEnd)
}

func TestJobs_OutputRefreshKeepsScrollPosition(t *testing.T) {
	t.Parallel()

	d := newJobsForTest(nil)
	d.ShowOutput(JobOutput{JobID: "001", Output: "a\nb\nc\nd\ne"})
	d.height = 2
	d.scrollOutput(-10)
	require.Zero(t, d.outputAt)

	d.ShowOutput(JobOutput{JobID: "001", Output: "a\nb\nc\nd\ne\nf\ng"})
	require.Zero(t, d.outputAt, "a poll must not yank the view to the tail")

	d.ShowOutput(JobOutput{JobID: "002", Output: "other"})
	require.True(t, d.outputEnd, "switching jobs starts at the newest output")
}
