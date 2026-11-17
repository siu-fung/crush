package lsp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/csync"
	"github.com/sourcegraph/jsonrpc2"
	"github.com/stretchr/testify/require"
)

// healthFixture is a Manager with one ready client per workspace, a probe
// whose verdict the test controls, and a restart counter. Nothing here
// starts a process: the probe and the restart are both faked.
type healthFixture struct {
	manager   *Manager
	probeErr  atomic.Pointer[error]
	restarts  atomic.Int32
	restarted sync.Map // workspace: restarted at least once
	now       time.Time
}

func newHealthFixture(t *testing.T, workspaces ...string) *healthFixture {
	t.Helper()

	f := &healthFixture{now: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)}
	f.manager = &Manager{
		clients:     csync.NewMap[string, *Client](),
		unavailable: csync.NewMap[string, time.Time](),
		now:         func() time.Time { return f.now },
		healthOpts: healthOptions{
			threshold:    3,
			probeTimeout: time.Second,
		},
	}
	// restartFn records which workspace was restarted and never talks
	// to a real server.
	f.manager.restartFn = func(client *Client) error {
		f.restarted.Store(client.cwd, true)
		f.restarts.Add(1)
		return nil
	}
	f.manager.probeFn = func(context.Context, *Client) error {
		if err := f.probeErr.Load(); err != nil {
			return *err
		}
		return nil
	}

	for _, workspace := range workspaces {
		client := &Client{name: "gopls", cwd: workspace}
		client.SetServerState(StateReady)
		// Key by workspace so two workspaces can share a server name.
		f.manager.clients.Set(workspace+"/gopls", client)
	}
	return f
}

func (f *healthFixture) fail(err error) {
	f.probeErr.Store(&err)
}

func (f *healthFixture) succeed() {
	f.probeErr.Store(nil)
}

func (f *healthFixture) client(workspace string) *Client {
	client, _ := f.manager.clients.Get(workspace + "/gopls")
	return client
}

func (f *healthFixture) round() {
	f.manager.CheckHealth(context.Background())
}

func waitRestarts(t *testing.T, f *healthFixture, want int32) {
	t.Helper()
	require.Eventually(t, func() bool {
		return f.restarts.Load() == want
	}, 2*time.Second, time.Millisecond)
}

// TestHealthRestartsAfterConsecutiveFailures pins the core contract: a
// client is probed every round, and it is restarted only once the
// failure streak reaches the threshold.
func TestHealthRestartsAfterConsecutiveFailures(t *testing.T) {
	t.Parallel()

	f := newHealthFixture(t, "/work")
	f.fail(errors.New("probe timed out"))

	f.round()
	f.round()
	require.Zero(t, f.restarts.Load(), "two failures are below the threshold")

	f.round()
	waitRestarts(t, f, 1)
}

// TestHealthSuccessClearsStreak covers the other half: a single good
// answer resets the streak, so a rare slow request never adds up to a
// restart.
func TestHealthSuccessClearsStreak(t *testing.T) {
	t.Parallel()

	f := newHealthFixture(t, "/work")
	f.fail(errors.New("probe timed out"))

	f.round()
	f.round()
	f.succeed()
	f.round()

	f.fail(errors.New("probe timed out"))
	f.round()
	f.round()
	require.Zero(t, f.restarts.Load(), "the streak must start over after a success")

	f.round()
	waitRestarts(t, f, 1)
}

// TestHealthRestartIsPerWorkspace is the per-workspace guarantee: one
// unhealthy workspace restarts only its own client, even though the
// server name is the same.
func TestHealthRestartIsPerWorkspace(t *testing.T) {
	t.Parallel()

	f := newHealthFixture(t, "/work-a", "/work-b")

	// Only workspace A's probe fails, so probeFn has to look at the
	// client to decide. Replacing it keeps the rest of the round intact.
	f.manager.probeFn = func(_ context.Context, client *Client) error {
		if client.cwd == "/work-a" {
			return errors.New("probe timed out")
		}
		return nil
	}

	for range 3 {
		f.round()
	}
	waitRestarts(t, f, 1)

	// Only workspace A's client may be restarted; B's must keep its
	// client and its zero failure streak.
	require.Eventually(t, func() bool {
		_, done := f.restarted.Load("/work-a")
		return done
	}, 2*time.Second, time.Millisecond)
	_, restartedB := f.restarted.Load("/work-b")
	require.False(t, restartedB, "the healthy workspace must keep its client")
	require.Equal(t, int32(1), f.restarts.Load())

	failures, _ := f.client("/work-b").healthFailures()
	require.Zero(t, failures, "the healthy workspace keeps a zero streak")
}

// TestHealthBackoffDelaysNextRestart checks that a server which keeps
// crashing is not respawned on every round.
func TestHealthBackoffDelaysNextRestart(t *testing.T) {
	t.Parallel()

	f := newHealthFixture(t, "/work")
	f.fail(errors.New("server crashed"))

	for range 3 {
		f.round()
	}
	waitRestarts(t, f, 1)

	// Immediately after a restart the next one is not due yet, however
	// many rounds fail in the meantime.
	for range 6 {
		f.round()
	}
	require.Equal(t, int32(1), f.restarts.Load(), "backoff must hold the restart")

	// Once the backoff has elapsed the client may be restarted again.
	f.now = f.now.Add(restartBackoff(1) + time.Second)
	for range 3 {
		f.round()
	}
	waitRestarts(t, f, 2)
}

// TestHealthFoldsRequestFailures covers a server that answers the probe
// but fails real requests: the request streak alone is enough to restart
// it.
func TestHealthFoldsRequestFailures(t *testing.T) {
	t.Parallel()

	f := newHealthFixture(t, "/work")
	f.succeed()

	client := f.client("/work")
	for range 3 {
		client.noteRequest(errors.New("request failed"))
	}

	// The probe keeps succeeding; the streak alone drives the restart,
	// and the record still needs the usual three failing rounds.
	for range 3 {
		f.round()
	}
	waitRestarts(t, f, 1)
}

// TestHealthIgnoresMethodNotFound pins that an unimplemented method
// proves the server is alive and must not count as a failure.
func TestHealthIgnoresMethodNotFound(t *testing.T) {
	t.Parallel()

	client := &Client{}
	for range 5 {
		client.noteRequest(&jsonrpc2.Error{Code: jsonrpcMethodNotFound, Message: "method not found"})
	}
	failures, _ := client.healthFailures()
	require.Zero(t, failures)

	client.noteRequest(context.Canceled)
	failures, _ = client.healthFailures()
	require.Zero(t, failures)

	client.noteRequest(errors.New("boom"))
	failures, _ = client.healthFailures()
	require.Equal(t, 1, failures)
}

// TestHealthSkipsClientsWithoutVerdict makes sure a client that is still
// starting is never probed: a slow start must not become a restart.
func TestHealthSkipsClientsWithoutVerdict(t *testing.T) {
	t.Parallel()

	f := newHealthFixture(t, "/work")
	f.fail(errors.New("probe timed out"))
	f.client("/work").SetServerState(StateStarting)

	for range 5 {
		f.round()
	}
	require.Zero(t, f.restarts.Load())
}

// TestRestartBackoffGrows checks the backoff schedule itself.
func TestRestartBackoffGrows(t *testing.T) {
	t.Parallel()

	require.Equal(t, healthBackoffBase, restartBackoff(1))
	require.Equal(t, 2*healthBackoffBase, restartBackoff(2))
	require.Equal(t, healthBackoffMax, restartBackoff(20))
}
