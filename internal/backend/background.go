package backend

import (
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/shell"
)

// ListBackgroundJobs returns the background shells this process knows
// about. The manager is process-wide rather than per workspace, so the
// workspace is only checked for existence: every frontend of this process
// sees the same jobs.
func (b *Backend) ListBackgroundJobs(workspaceID string) ([]proto.BackgroundJob, error) {
	if _, err := b.GetWorkspace(workspaceID); err != nil {
		return nil, err
	}

	infos := shell.GetBackgroundShellManager().Infos()
	jobs := make([]proto.BackgroundJob, len(infos))
	for i, info := range infos {
		jobs[i] = backgroundJobToProto(info)
	}
	return jobs, nil
}

// BackgroundJobOutput returns a job's snapshot along with the last lines it
// printed.
func (b *Backend) BackgroundJobOutput(workspaceID, jobID string) (proto.BackgroundJobOutput, error) {
	if _, err := b.GetWorkspace(workspaceID); err != nil {
		return proto.BackgroundJobOutput{}, err
	}

	info, output, err := shell.GetBackgroundShellManager().Output(jobID)
	if err != nil {
		return proto.BackgroundJobOutput{}, err
	}
	return proto.BackgroundJobOutput{
		Job:    backgroundJobToProto(info),
		Output: output,
	}, nil
}

// KillBackgroundJob terminates a background shell. The shell's cancellation
// reaches the exec handler, which signals the child's whole process group
// so grandchildren die with it.
func (b *Backend) KillBackgroundJob(workspaceID, jobID string) error {
	if _, err := b.GetWorkspace(workspaceID); err != nil {
		return err
	}
	return shell.GetBackgroundShellManager().Kill(jobID)
}

func backgroundJobToProto(info shell.JobInfo) proto.BackgroundJob {
	return proto.BackgroundJob{
		ID:          info.ID,
		Command:     info.Command,
		Description: info.Description,
		WorkingDir:  info.WorkingDir,
		Running:     info.Running,
		ExitCode:    info.ExitCode,
		StartedAt:   info.StartedAt,
		CompletedAt: info.CompletedAt,
	}
}
