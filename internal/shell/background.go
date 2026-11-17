package shell

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// MaxFinishedJobs bounds how many finished jobs keep their output
	// readable. The oldest are dropped first.
	MaxFinishedJobs = 250
	// CompletedJobRetention is how long a finished job's output stays
	// readable.
	CompletedJobRetention = 8 * time.Hour
)

// ErrJobNotFound reports a job ID the manager no longer tracks, either
// because it finished long enough ago to be pruned or because it was
// never started here.
var ErrJobNotFound = errors.New("background shell not found")

// BackgroundShell represents a shell running in the background.
type BackgroundShell struct {
	ID          string
	Command     string
	Description string
	Shell       *Shell
	WorkingDir  string
	ctx         context.Context
	cancel      context.CancelFunc
	// stdout and stderr keep the full byte stream (bounded by
	// maxKeptOutput); the tails keep the last JobTailLines lines, which
	// is what the job panel and job_output display.
	stdout      *cappedBuffer
	stderr      *cappedBuffer
	stdoutTail  *lineRing
	stderrTail  *lineRing
	done        chan struct{}
	exitErr     error
	startedAt   int64        // Unix timestamp when the job was started
	completedAt atomic.Int64 // Unix timestamp when job completed (0 if still running)
}

// BackgroundShellManager manages background shell instances.
type BackgroundShellManager struct {
	mu     sync.Mutex
	shells map[string]*BackgroundShell
}

var (
	backgroundManager     *BackgroundShellManager
	backgroundManagerOnce sync.Once
	idCounter             atomic.Uint64
)

// newBackgroundShellManager creates a new BackgroundShellManager instance.
func newBackgroundShellManager() *BackgroundShellManager {
	return &BackgroundShellManager{shells: map[string]*BackgroundShell{}}
}

// GetBackgroundShellManager returns the singleton background shell manager.
func GetBackgroundShellManager() *BackgroundShellManager {
	backgroundManagerOnce.Do(func() {
		backgroundManager = newBackgroundShellManager()
	})
	return backgroundManager
}

// Start creates and starts a new background shell with the given command,
// unless maxJobs jobs are already running.
func (m *BackgroundShellManager) Start(ctx context.Context, workingDir string, blockFuncs []BlockFunc, command, description string, maxJobs int) (*BackgroundShell, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if running := m.prune(); running >= maxJobs {
		return nil, fmt.Errorf("maximum number of running background jobs (%d) reached. Please terminate or wait for some jobs to complete", maxJobs)
	}

	id := fmt.Sprintf("%03X", idCounter.Add(1))

	shell := NewShell(&Options{
		WorkingDir: workingDir,
		BlockFuncs: blockFuncs,
	})

	shellCtx, cancel := context.WithCancel(ctx)

	bgShell := &BackgroundShell{
		ID:          id,
		Command:     command,
		Description: description,
		WorkingDir:  workingDir,
		Shell:       shell,
		ctx:         shellCtx,
		cancel:      cancel,
		stdout:      newCappedBuffer(maxKeptOutput),
		stderr:      newCappedBuffer(maxKeptOutput),
		stdoutTail:  newLineRing(JobTailLines),
		stderrTail:  newLineRing(JobTailLines),
		done:        make(chan struct{}),
		startedAt:   time.Now().Unix(),
	}

	m.shells[id] = bgShell

	go func() {
		defer close(bgShell.done)

		// Tee every write into the byte buffer and the line ring so a
		// reader never has to re-parse a megabyte to find the tail.
		stdout := io.MultiWriter(bgShell.stdout, bgShell.stdoutTail)
		stderr := io.MultiWriter(bgShell.stderr, bgShell.stderrTail)
		err := shell.ExecStream(shellCtx, command, stdout, stderr)

		bgShell.exitErr = err
		bgShell.completedAt.Store(time.Now().Unix())
	}()

	return bgShell, nil
}

// prune drops finished jobs that are past retention or beyond
// MaxFinishedJobs, and reports how many jobs are still running. Callers
// must hold m.mu.
func (m *BackgroundShellManager) prune() (running int) {
	cutoff := time.Now().Add(-CompletedJobRetention).Unix()
	var finished []*BackgroundShell
	for id, shell := range m.shells {
		switch at := shell.completedAt.Load(); {
		case at == 0:
			running++
		case at < cutoff:
			delete(m.shells, id)
		default:
			finished = append(finished, shell)
		}
	}

	if over := len(finished) - MaxFinishedJobs; over > 0 {
		slices.SortFunc(finished, func(a, b *BackgroundShell) int {
			return cmp.Compare(a.completedAt.Load(), b.completedAt.Load())
		})
		for _, shell := range finished[:over] {
			delete(m.shells, shell.ID)
		}
	}
	return running
}

// take removes and returns the shell with the given ID.
func (m *BackgroundShellManager) take(id string) (*BackgroundShell, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	shell, ok := m.shells[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrJobNotFound, id)
	}
	delete(m.shells, id)
	return shell, nil
}

// Get retrieves a background shell by ID.
func (m *BackgroundShellManager) Get(id string) (*BackgroundShell, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	shell, ok := m.shells[id]
	return shell, ok
}

// Remove stops tracking a background shell without terminating it.
func (m *BackgroundShellManager) Remove(id string) error {
	_, err := m.take(id)
	return err
}

// Kill terminates a background shell by ID.
func (m *BackgroundShellManager) Kill(id string) error {
	shell, err := m.take(id)
	if err != nil {
		return err
	}
	shell.cancel()
	<-shell.done
	return nil
}

// BackgroundShellInfo contains information about a background shell.
type BackgroundShellInfo struct {
	ID          string
	Command     string
	Description string
}

// List returns all background shell IDs.
func (m *BackgroundShellManager) List() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Collect(maps.Keys(m.shells))
}

// KillAll terminates all background shells. The provided context bounds how
// long the function waits for each shell to exit.
func (m *BackgroundShellManager) KillAll(ctx context.Context) {
	m.mu.Lock()
	shells := m.shells
	m.shells = map[string]*BackgroundShell{}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, shell := range shells {
		wg.Go(func() {
			shell.cancel()
			select {
			case <-shell.done:
			case <-ctx.Done():
			}
		})
	}
	wg.Wait()
}

// GetOutput returns the current output of a background shell.
func (bs *BackgroundShell) GetOutput() (stdout string, stderr string, done bool, err error) {
	select {
	case <-bs.done:
		return bs.stdout.String(), bs.stderr.String(), true, bs.exitErr
	default:
		return bs.stdout.String(), bs.stderr.String(), false, nil
	}
}

// GetRecentOutput returns the last JobTailLines lines of each stream. It is
// the read a live panel wants: bounded by lines rather than bytes, so it
// stays cheap while the job is still writing.
func (bs *BackgroundShell) GetRecentOutput() (stdout, stderr string, done bool, err error) {
	if bs.stdoutTail != nil {
		stdout = bs.stdoutTail.String()
	}
	if bs.stderrTail != nil {
		stderr = bs.stderrTail.String()
	}
	select {
	case <-bs.done:
		return stdout, stderr, true, bs.exitErr
	default:
		return stdout, stderr, false, nil
	}
}

// JobInfo is a snapshot of a background shell for listing and inspection.
type JobInfo struct {
	ID          string `json:"id"`
	Command     string `json:"command"`
	Description string `json:"description"`
	WorkingDir  string `json:"working_dir"`
	Running     bool   `json:"running"`
	ExitCode    int    `json:"exit_code"`
	StartedAt   int64  `json:"started_at"`
	CompletedAt int64  `json:"completed_at"`
}

// Info snapshots the job for display. The exit error is only read once done
// is closed, so it cannot race the goroutine that sets it.
func (bs *BackgroundShell) Info() JobInfo {
	info := JobInfo{
		ID:          bs.ID,
		Command:     bs.Command,
		Description: bs.Description,
		WorkingDir:  bs.WorkingDir,
		StartedAt:   bs.startedAt,
		CompletedAt: bs.completedAt.Load(),
	}
	select {
	case <-bs.done:
		info.ExitCode = ExitCode(bs.exitErr)
	default:
		info.Running = true
	}
	return info
}

// Infos snapshots every tracked job. Running jobs come first, then the most
// recently started, which is the order a job panel wants.
func (m *BackgroundShellManager) Infos() []JobInfo {
	m.mu.Lock()
	shells := slices.Collect(maps.Values(m.shells))
	m.mu.Unlock()

	infos := make([]JobInfo, len(shells))
	for i, sh := range shells {
		infos[i] = sh.Info()
	}
	slices.SortFunc(infos, func(a, b JobInfo) int {
		if a.Running != b.Running {
			if a.Running {
				return -1
			}
			return 1
		}
		return cmp.Compare(b.StartedAt, a.StartedAt)
	})
	return infos
}

// Output returns a job's snapshot along with its retained tail. Both
// streams are concatenated, stdout first, because that is how a command's
// output reads when interleaved in a terminal.
func (m *BackgroundShellManager) Output(id string) (JobInfo, string, error) {
	shell, ok := m.Get(id)
	if !ok {
		return JobInfo{}, "", fmt.Errorf("%w: %s", ErrJobNotFound, id)
	}
	stdout, stderr, _, _ := shell.GetRecentOutput()
	return shell.Info(), joinStreams(stdout, stderr), nil
}

// joinStreams concatenates the two streams, skipping the empty one.
func joinStreams(stdout, stderr string) string {
	switch {
	case stdout == "":
		return stderr
	case stderr == "":
		return stdout
	default:
		return stdout + "\n" + stderr
	}
}

// IsDone checks if the background shell has finished execution.
func (bs *BackgroundShell) IsDone() bool {
	select {
	case <-bs.done:
		return true
	default:
		return false
	}
}

// Done returns a channel that is closed when the background shell exits.
func (bs *BackgroundShell) Done() <-chan struct{} {
	return bs.done
}

// Wait blocks until the background shell completes.
func (bs *BackgroundShell) Wait() {
	<-bs.done
}

func (bs *BackgroundShell) WaitContext(ctx context.Context) bool {
	select {
	case <-bs.done:
		return true
	case <-ctx.Done():
		return false
	}
}
