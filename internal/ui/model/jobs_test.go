package model

import (
	"errors"
	"testing"

	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/stretchr/testify/require"
)

// newTestUIForJobs builds a UI with a dialog overlay and the background
// jobs panel open.
func newTestUIForJobs() (*UI, *dialog.Jobs) {
	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	jobs := dialog.NewJobs(u.com)
	u.dialog.OpenDialog(jobs)
	return u, jobs
}

func TestJobsTickFollowsPanel(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	require.Empty(t, u.applyJobsTick(), "a closed panel must not keep polling")

	u.dialog.OpenDialog(dialog.NewJobs(u.com))
	require.Len(t, u.applyJobsTick(), 2, "an open panel polls and re-arms the tick")
}

func TestApplyJobsLoadedReportsFailureOnce(t *testing.T) {
	t.Parallel()

	u, _ := newTestUIForJobs()

	first := u.applyJobsLoaded(jobsLoadedMsg{err: errors.New("server unreachable")})
	require.Len(t, first, 1, "the first failure is reported")

	require.Empty(t, u.applyJobsLoaded(jobsLoadedMsg{err: errors.New("server unreachable")}),
		"an unchanged failure must not stack another toast")

	require.Empty(t, u.applyJobsLoaded(jobsLoadedMsg{}))
	require.Empty(t, u.jobsPollErr, "a successful read clears the failure")
}

func TestApplyJobOutputIgnoresStalePoll(t *testing.T) {
	t.Parallel()

	u, jobs := newTestUIForJobs()
	jobs.ShowOutput(dialog.JobOutput{JobID: "001", Running: true, Output: "hi"})
	jobs.ShowList()

	require.Nil(t, u.applyJobOutput(jobOutputMsg{jobID: "001"}),
		"a poll that lands after the user went back must not reopen the view")
	require.False(t, jobs.InOutput())

	require.Nil(t, u.applyJobOutput(jobOutputMsg{jobID: "001", requested: true}))
	require.True(t, jobs.InOutput(), "an explicit selection opens the output")
	require.Equal(t, "001", jobs.OutputJobID())
}
