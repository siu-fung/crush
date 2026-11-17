package lsp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/sourcegraph/jsonrpc2"
)

// Language servers fail in ways nobody reports: the process exits, a
// request never comes back, or the server wedges itself while its
// connection stays open. The health monitor probes every running client
// on a timer, folds in the request failures each client already saw, and
// restarts a client that keeps failing. Restarts are scoped to one
// (workspace, server) pair, so an unhealthy workspace cannot take another
// workspace's server down with it.
const (
	// DefaultHealthInterval is how often every client is probed.
	DefaultHealthInterval = 15 * time.Second

	// DefaultHealthFailureThreshold is how many consecutive failures a
	// client may show before it is restarted. Three checks is enough to
	// tell a wedged server from one busy with a single slow request.
	DefaultHealthFailureThreshold = 3

	// healthProbeTimeout bounds the probe request. A server that cannot
	// answer within it counts as failed for this round.
	healthProbeTimeout = 5 * time.Second

	// Restart backoff: doubling from the base up to the cap, so a server
	// that crashes on startup is not respawned every 15 seconds forever.
	healthBackoffBase = 30 * time.Second
	healthBackoffMax  = 10 * time.Minute

	// jsonrpcMethodNotFound is the JSON-RPC error code a server returns
	// for a method it does not implement.
	jsonrpcMethodNotFound = -32601
)

// healthOptions configures the monitor. Zero values fall back to the
// defaults above.
type healthOptions struct {
	interval     time.Duration
	threshold    int
	probeTimeout time.Duration
}

func (o healthOptions) withDefaults() healthOptions {
	if o.interval <= 0 {
		o.interval = DefaultHealthInterval
	}
	if o.threshold <= 0 {
		o.threshold = DefaultHealthFailureThreshold
	}
	if o.probeTimeout <= 0 {
		o.probeTimeout = healthProbeTimeout
	}
	return o
}

// clientHealth is the health record of one (workspace, server) pair. Its
// counters survive restarts so the backoff can grow across them.
type clientHealth struct {
	mu          sync.Mutex
	workspace   string
	server      string
	failures    int
	restarts    int
	lastError   string
	nextRestart time.Time
	restarting  bool
}

// noteFailure records a failed check and reports whether a restart is
// due. After a restart the next one is delayed by an exponential
// backoff, so a permanently broken server is not respawned in a loop.
func (r *clientHealth) noteFailure(err error, threshold int, now time.Time) (failures int, restart bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.restarting {
		return r.failures, false
	}
	r.failures++
	r.lastError = err.Error()
	if r.failures < threshold || now.Before(r.nextRestart) {
		return r.failures, false
	}
	r.restarting = true
	return r.failures, true
}

// noteSuccess clears the failure streak, reporting the streak it cleared
// so the caller can log it once.
func (r *clientHealth) noteSuccess() (failures int, lastError string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.failures == 0 {
		return 0, ""
	}
	failures, lastError = r.failures, r.lastError
	r.failures, r.lastError = 0, ""
	return failures, lastError
}

// finishRestart closes out a restart attempt and schedules the earliest
// time the next one may run.
func (r *clientHealth) finishRestart(err error, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.restarting = false
	r.failures = 0
	r.restarts++
	if err != nil {
		r.lastError = err.Error()
	}
	r.nextRestart = now.Add(restartBackoff(r.restarts))
}

func (r *clientHealth) snapshot() (restarts int, lastError string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.restarts, r.lastError
}

// restartBackoff returns how long to wait before the n-th restart.
func restartBackoff(restarts int) time.Duration {
	if restarts < 1 {
		restarts = 1
	}
	backoff := healthBackoffBase
	for i := 1; i < restarts; i++ {
		backoff *= 2
		if backoff >= healthBackoffMax {
			return healthBackoffMax
		}
	}
	return backoff
}

// StartHealthMonitor starts the background monitor that keeps LSP
// clients alive. It runs until ctx is cancelled and is safe to call more
// than once: only the first call starts a monitor.
func (s *Manager) StartHealthMonitor(ctx context.Context) {
	s.monitorOnce.Do(func() {
		go s.monitorLoop(ctx)
	})
}

func (s *Manager) monitorLoop(ctx context.Context) {
	opts := s.healthOpts.withDefaults()
	slog.Debug("LSP health monitor started",
		"interval", opts.interval, "threshold", opts.threshold, "probe_timeout", opts.probeTimeout)

	ticker := time.NewTicker(opts.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkHealth(ctx, opts)
		}
	}
}

// CheckHealth probes every client once and restarts those that have
// failed too many times in a row. The monitor calls it on a timer; it is
// exported so diagnostics and tests can run a single round on demand.
func (s *Manager) CheckHealth(ctx context.Context) {
	s.checkHealth(ctx, s.healthOpts)
}

func (s *Manager) checkHealth(ctx context.Context, opts healthOptions) {
	if s.clients == nil {
		return
	}
	opts = opts.withDefaults()
	for name, client := range s.clients.Seq2() {
		s.checkClient(ctx, opts, name, client)
	}
}

func (s *Manager) checkClient(ctx context.Context, opts healthOptions, name string, client *Client) {
	if client == nil {
		return
	}
	// A client that is still starting or already shutting down has no
	// meaningful health verdict to give.
	switch client.GetServerState() {
	case StateStarting, StateStopped, StateDisabled:
		return
	}

	record := s.healthRecord(client.cwd, name)

	probeCtx, cancel := context.WithTimeout(ctx, opts.probeTimeout)
	defer cancel()

	// Two independent signals: the probe reports whether the server
	// still answers at all, and the request streak reports whether its
	// answers are usable. Either failing long enough earns a restart.
	probeErr := s.probeClient(probeCtx, client)
	reqFailures, reqErr := client.healthFailures()

	var err error
	switch {
	case probeErr != nil:
		err = probeErr
	case reqFailures >= opts.threshold:
		err = fmt.Errorf("%d consecutive requests failed, last error: %s", reqFailures, reqErr)
	}
	if err == nil {
		// A healthy round ends the request streak: whatever failed
		// before, the server is answering now.
		client.noteRequest(nil)
		if failures, lastError := record.noteSuccess(); failures > 0 {
			slog.Info("LSP client recovered",
				"workspace", record.workspace, "name", name,
				"cleared_failures", failures, "last_error", lastError)
		}
		return
	}

	failures, restart := record.noteFailure(err, opts.threshold, s.nowTime())
	slog.Warn("LSP health check failed",
		"workspace", record.workspace, "name", name,
		"failures", failures, "threshold", opts.threshold, "error", err)
	if restart {
		go s.restartClient(client, record)
	}
}

// probeClient returns the health verdict for one client: a client whose
// process is gone or that reports an error state has failed without a
// request, and otherwise an active request decides.
func (s *Manager) probeClient(ctx context.Context, client *Client) error {
	if s.probeFn != nil {
		return s.probeFn(ctx, client)
	}
	switch client.GetServerState() {
	case StateError:
		return errors.New("language server is in an error state")
	}
	if !client.IsRunning() {
		return errors.New("language server process is not running")
	}
	return client.Probe(ctx)
}

// restartClient restarts one client and records the outcome. It runs off
// the monitor goroutine because a restart takes as long as the server
// needs to come back.
func (s *Manager) restartClient(client *Client, record *clientHealth) {
	name, workspace := client.GetName(), client.cwd
	restarts, lastError := record.snapshot()
	slog.Warn("Restarting unhealthy LSP client",
		"workspace", workspace, "name", name,
		"restart", restarts+1, "last_error", lastError)

	err := s.runRestart(client)
	record.finishRestart(err, s.nowTime())
	if err != nil {
		slog.Error("Failed to restart LSP client",
			"workspace", workspace, "name", name,
			"restart", restarts+1, "error", err)
	} else {
		slog.Info("Restarted LSP client",
			"workspace", workspace, "name", name,
			"restart", restarts+1, "next_allowed_in", restartBackoff(restarts+1))
	}

	// Announce the new state so the UI and the coordinator see a live
	// client again instead of the stale one they last heard about.
	if s.callback != nil {
		s.callback(name, client)
	}
}

func (s *Manager) runRestart(client *Client) error {
	if s.restartFn != nil {
		return s.restartFn(client)
	}
	return client.Restart()
}

// healthRecord returns the record for one (workspace, server) pair,
// creating it on first use. The pair is the key: two workspaces served by
// the same binary keep independent failure streaks and backoffs.
func (s *Manager) healthRecord(workspace, name string) *clientHealth {
	s.healthMu.Lock()
	if s.health == nil {
		s.health = csync.NewMap[string, *clientHealth]()
	}
	records := s.health
	s.healthMu.Unlock()

	key := workspace + "\x00" + name
	return records.GetOrSet(key, func() *clientHealth {
		return &clientHealth{workspace: workspace, server: name}
	})
}

func (s *Manager) nowTime() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// healthStats is the request-level failure streak of one client. Every
// request path reports into it, and the monitor reads it: a server that
// keeps answering badly is as unhealthy as one that stopped answering.
type healthStats struct {
	mu       sync.Mutex
	failures int
	lastErr  string
}

func (c *Client) noteRequest(err error) {
	// A method the server does not implement, or a request the caller
	// cancelled, says nothing about how healthy the server is.
	if err != nil && (isMethodNotFound(err) || errors.Is(err, context.Canceled)) {
		return
	}
	c.health.mu.Lock()
	defer c.health.mu.Unlock()

	if err == nil {
		c.health.failures = 0
		c.health.lastErr = ""
		return
	}
	c.health.failures++
	c.health.lastErr = err.Error()
}

// healthFailures reports how many requests failed in a row. A successful
// request clears the streak.
func (c *Client) healthFailures() (failures int, lastError string) {
	c.health.mu.Lock()
	defer c.health.mu.Unlock()
	return c.health.failures, c.health.lastErr
}

// IsRunning reports whether the server process is still alive and its
// connection still open.
func (c *Client) IsRunning() bool {
	return c != nil && c.client != nil && c.client.IsRunning()
}

// Probe checks that the server answers a request. Document symbols for an
// already open file is the cheapest request every server understands;
// "method not found" still proves the round trip works, while a timeout
// or a closed connection is what the health monitor looks for.
func (c *Client) Probe(ctx context.Context) error {
	if c == nil {
		return nil
	}
	path := c.anyOpenFilePath()
	if path == "" {
		// Nothing is open, so there is no document-level request to
		// make; process liveness is the only signal, and the caller
		// already checked it.
		return nil
	}
	if _, err := c.client.RequestDocumentSymbols(ctx, path); err != nil {
		if isMethodNotFound(err) {
			return nil
		}
		return err
	}
	return nil
}

// anyOpenFilePath returns one open document's path, or "" when none is
// open.
func (c *Client) anyOpenFilePath() string {
	for uri := range c.openFiles.Seq2() {
		if path, err := documentURIPath(uri); err == nil {
			return path
		}
	}
	return ""
}

// isMethodNotFound reports whether err is the JSON-RPC error a server
// sends for an unimplemented method. Such an answer proves the server is
// alive, so it must not count as a health failure.
func isMethodNotFound(err error) bool {
	var rpcErr *jsonrpc2.Error
	return errors.As(err, &rpcErr) && rpcErr.Code == jsonrpcMethodNotFound
}

// documentURIPath converts an open document's URI key back to a path.
func documentURIPath(uri string) (string, error) {
	return protocol.DocumentURI(uri).Path()
}
