package model

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/workspace"
)

// jobsTickRate is how often the open background jobs panel re-reads job
// state. A second is fast enough to look live without hammering the
// workspace (each read is an HTTP round-trip in client/server mode).
const jobsTickRate = time.Second

// jobsLoadedMsg carries a background job list snapshot fetched off the
// Update goroutine.
type jobsLoadedMsg struct {
	entries []dialog.JobEntry
	err     error
}

// jobOutputMsg carries a job's retained output tail. requested marks a read
// the user asked for by selecting the job, as opposed to a poll: a poll that
// lands after the user went back to the list must not reopen the view.
type jobOutputMsg struct {
	jobID     string
	requested bool
	output    dialog.JobOutput
	err       error
}

// jobKilledMsg reports the outcome of a kill request.
type jobKilledMsg struct {
	jobID string
	err   error
}

// jobsTickMsg asks the open panel to poll once more.
type jobsTickMsg struct{}

// jobsDialog returns the open background jobs panel, or nil.
func (m *UI) jobsDialog() *dialog.Jobs {
	dia := m.dialog.Dialog(dialog.JobsID)
	if dia == nil {
		return nil
	}
	jobs, _ := dia.(*dialog.Jobs)
	return jobs
}

// openJobsDialog opens the background jobs panel and starts the poll that
// keeps it current. The list itself arrives asynchronously so the Update
// goroutine never waits on the workspace.
func (m *UI) openJobsDialog() tea.Cmd {
	if m.jobsDialog() != nil {
		m.dialog.BringToFront(dialog.JobsID)
		return nil
	}
	if m.com == nil || m.com.Workspace == nil {
		return nil
	}
	m.dialog.OpenDialog(dialog.NewJobs(m.com))
	return tea.Batch(m.refreshJobsCmd(), m.jobsTickCmd())
}

// refreshJobsCmd fetches the job list off the Update goroutine.
func (m *UI) refreshJobsCmd() tea.Cmd {
	if m.com == nil || m.com.Workspace == nil {
		return nil
	}
	ws := m.com.Workspace
	return func() tea.Msg {
		jobs, err := ws.ListBackgroundJobs(context.Background())
		if err != nil {
			return jobsLoadedMsg{err: err}
		}
		entries := make([]dialog.JobEntry, 0, len(jobs))
		for _, job := range jobs {
			entries = append(entries, jobEntryFromWorkspace(job))
		}
		return jobsLoadedMsg{entries: entries}
	}
}

// refreshJobOutputCmd fetches one job's retained tail off the Update
// goroutine. requested distinguishes a selection from a poll.
func (m *UI) refreshJobOutputCmd(jobID string, requested bool) tea.Cmd {
	if jobID == "" || m.com == nil || m.com.Workspace == nil {
		return nil
	}
	ws := m.com.Workspace
	return func() tea.Msg {
		out, err := ws.BackgroundJobOutput(context.Background(), jobID)
		if err != nil {
			return jobOutputMsg{jobID: jobID, requested: requested, err: err}
		}
		return jobOutputMsg{
			jobID:     jobID,
			requested: requested,
			output: dialog.JobOutput{
				JobID:    jobID,
				Command:  out.Job.Command,
				Running:  out.Job.Running,
				ExitCode: out.Job.ExitCode,
				Output:   out.Output,
			},
		}
	}
}

// killJobCmd terminates a background job, and every process it spawned,
// off the Update goroutine: the kill signals the process group and waits
// for the shell to exit.
func (m *UI) killJobCmd(jobID string) tea.Cmd {
	if jobID == "" || m.com == nil || m.com.Workspace == nil {
		return nil
	}
	ws := m.com.Workspace
	return func() tea.Msg {
		return jobKilledMsg{jobID: jobID, err: ws.KillBackgroundJob(context.Background(), jobID)}
	}
}

// jobsTickCmd schedules the next poll.
func (m *UI) jobsTickCmd() tea.Cmd {
	return tea.Tick(jobsTickRate, func(time.Time) tea.Msg {
		return jobsTickMsg{}
	})
}

// applyJobsTick polls the open panel once and re-arms the tick. Closing
// the panel stops the loop, since there is nothing left to refresh.
func (m *UI) applyJobsTick() []tea.Cmd {
	jobs := m.jobsDialog()
	if jobs == nil {
		return nil
	}
	var cmds []tea.Cmd
	if jobID := jobs.OutputJobID(); jobID != "" {
		cmds = append(cmds, m.refreshJobOutputCmd(jobID, false))
	} else {
		cmds = append(cmds, m.refreshJobsCmd())
	}
	cmds = append(cmds, m.jobsTickCmd())
	return cmds
}

// applyJobsLoaded stores a job snapshot in the open panel.
func (m *UI) applyJobsLoaded(msg jobsLoadedMsg) []tea.Cmd {
	if msg.err != nil {
		// The poll repeats every second, so report a failure only when
		// it changes instead of stacking identical toasts.
		text := msg.err.Error()
		if m.jobsPollErr == text {
			return nil
		}
		m.jobsPollErr = text
		return []tea.Cmd{util.ReportWarn("Could not read background jobs: " + text)}
	}
	m.jobsPollErr = ""
	jobs := m.jobsDialog()
	if jobs == nil {
		return nil
	}
	jobs.SetJobs(msg.entries)
	return nil
}

// applyJobOutput shows freshly read output, unless the user has already
// switched to another job or gone back to the list.
func (m *UI) applyJobOutput(msg jobOutputMsg) []tea.Cmd {
	jobs := m.jobsDialog()
	if jobs == nil {
		return nil
	}
	if jobs.InOutput() {
		if jobs.OutputJobID() != msg.jobID {
			return nil
		}
	} else if !msg.requested {
		// The user left the output view while this poll was in flight.
		return nil
	}
	if msg.err != nil {
		jobs.ShowList()
		cmds := []tea.Cmd{util.ReportWarn("Job #" + msg.jobID + " is no longer available.")}
		if cmd := m.refreshJobsCmd(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return cmds
	}
	if msg.output.JobID == "" {
		msg.output.JobID = msg.jobID
	}
	jobs.ShowOutput(msg.output)
	return nil
}

// applyJobKilled drops the killed job from the panel and reports the
// outcome.
func (m *UI) applyJobKilled(msg jobKilledMsg) []tea.Cmd {
	if msg.err != nil {
		cmds := []tea.Cmd{util.ReportWarn("Could not kill job #" + msg.jobID + ": " + msg.err.Error())}
		if cmd := m.refreshJobsCmd(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return cmds
	}
	cmds := []tea.Cmd{util.ReportInfo("Killed background job #" + msg.jobID + ".")}
	jobs := m.jobsDialog()
	if jobs != nil && jobs.OutputJobID() == msg.jobID {
		// The job is gone from the manager, so its output view has
		// nothing left to show.
		jobs.ShowList()
	}
	if cmd := m.refreshJobsCmd(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return cmds
}

// jobEntryFromWorkspace converts a workspace job snapshot into a panel
// entry.
func jobEntryFromWorkspace(job workspace.BackgroundJob) dialog.JobEntry {
	return dialog.JobEntry{
		ID:          job.ID,
		Command:     job.Command,
		Description: job.Description,
		WorkingDir:  job.WorkingDir,
		Running:     job.Running,
		ExitCode:    job.ExitCode,
		StartedAt:   job.StartedAt,
		CompletedAt: job.CompletedAt,
	}
}
