package hooks

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/shell"
)

// abandonGrace is how long runOne waits after ctx cancellation for the
// shell goroutine to yield before returning control to the caller and
// letting the goroutine finish on its own. Mirrors the historical
// cmd.WaitDelay = time.Second behavior of the previous os/exec path.
const abandonGrace = time.Second

// runShell is the shell executor used by runOne. It is a package-level
// variable so tests can substitute a blocking or non-yielding
// implementation to exercise the abandon-on-timeout path without
// depending on the scheduling behavior of the real interpreter.
var runShell = shell.Run

// Source returns the current hook configuration keyed by event name.
// The runner calls it on every event, so a source backed by the config
// store picks up reloads with no extra wiring.
type Source func() map[string][]HookConfig

// Static returns a Source that always yields the same hooks.
func Static(hooks map[string][]HookConfig) Source {
	return func() map[string][]HookConfig { return hooks }
}

// Runner executes hook commands and aggregates their results. One
// Runner serves every event for the application. A nil Runner is valid
// and runs nothing, so callers never have to guard against it.
type Runner struct {
	source     Source
	cwd        string
	projectDir string

	matchersMu sync.Mutex
	matchers   map[string]*regexp.Regexp // nil value: pattern did not compile

	// limitFn reports the per-turn continuation budget. Nil, or a
	// non-positive value, means uncapped.
	limitFn func() int

	contMu        sync.Mutex
	continuations map[string]int // session ID: continuations used this turn
}

// NewRunner creates a Runner that reads hook configs from source.
// Matchers are compiled lazily and cached by pattern; one that fails to
// compile is skipped with a warning rather than treated as
// match-everything. ValidateHooks is expected to have caught syntax
// errors earlier, so this is defense in depth.
func NewRunner(source Source, cwd, projectDir string) *Runner {
	return &Runner{
		source:        source,
		cwd:           cwd,
		projectDir:    projectDir,
		matchers:      make(map[string]*regexp.Regexp),
		continuations: make(map[string]int),
	}
}

// SetContinuationLimit installs the per-turn continuation budget. It is
// read on every event so a config reload takes effect without rewiring.
// A nil function, or one that returns a non-positive value, disables the
// cap.
func (r *Runner) SetContinuationLimit(limit func() int) {
	if r == nil {
		return
	}
	r.limitFn = limit
}

// Run executes every configured hook for the event in parallel and
// returns their aggregated result. Hooks with a matcher only run when
// it matches the event's tool name. Execution failures are logged and
// treated as "no opinion", so the result is always usable.
func (r *Runner) Run(ctx context.Context, ev Event) AggregateResult {
	if r == nil {
		return AggregateResult{}
	}
	r.trackTurnBoundary(ev)
	hooks := r.matchingHooks(ev)
	if len(hooks) == 0 {
		return AggregateResult{}
	}

	envVars := BuildEnv(ev, r.cwd, r.projectDir)
	payload := BuildPayload(ev, r.cwd)

	results := make([]HookResult, len(hooks))
	var wg sync.WaitGroup
	for i, h := range hooks {
		wg.Go(func() {
			results[i] = r.runOne(ctx, h, envVars, payload)
		})
	}
	wg.Wait()

	var toolInput string
	if ev.Tool != nil {
		toolInput = ev.Tool.Input
	}
	agg := aggregate(results, toolInput)
	agg = r.capContinuation(ev, agg)
	agg.Hooks = make([]HookInfo, len(hooks))
	for i, h := range hooks {
		agg.Hooks[i] = HookInfo{
			Name:          h.DisplayName(),
			Event:         ev.Name,
			Matcher:       h.Matcher,
			Decision:      results[i].Decision.String(),
			Halt:          results[i].Halt,
			Reason:        results[i].Reason,
			InputRewrite:  results[i].UpdatedInput != "",
			OutputRewrite: results[i].UpdatedOutput != "",
		}
	}
	slog.Info(
		"Hook completed",
		"event", ev.Name,
		"tool", ev.toolName(),
		"hooks", agg.HookCount,
		"decision", agg.Decision.String(),
	)
	return agg
}

// trackTurnBoundary clears a session's continuation budget when a turn
// starts, and forgets it when the turn ends, so the budget is always
// per-turn and the map does not grow with every session.
func (r *Runner) trackTurnBoundary(ev Event) {
	switch ev.Name {
	case EventTurnStart, EventTurnEnd, EventInterrupt, EventStopFailure:
		r.contMu.Lock()
		delete(r.continuations, ev.SessionID)
		r.contMu.Unlock()
	}
}

// capContinuation spends one unit of the turn's continuation budget for
// an event whose hooks asked the model to keep going. Once the budget is
// gone the hooks' context is replaced with a stop instruction, so a
// command that keeps failing cannot loop forever. Continuations on
// events that carry no tool call are ignored: their context never
// reaches the model.
func (r *Runner) capContinuation(ev Event, agg AggregateResult) AggregateResult {
	if !agg.Continuation || !EventHasTool(ev.Name) {
		return agg
	}
	var limit int
	if r.limitFn != nil {
		limit = r.limitFn()
	}
	if limit <= 0 {
		return agg
	}
	r.contMu.Lock()
	r.continuations[ev.SessionID]++
	used := r.continuations[ev.SessionID]
	r.contMu.Unlock()
	if used <= limit {
		return agg
	}
	slog.Info(
		"Hook continuation limit reached",
		"session", ev.SessionID,
		"event", ev.Name,
		"tool", ev.toolName(),
		"used", used,
		"limit", limit,
	)
	agg.Context = fmt.Sprintf(
		"Hook continuation limit reached (%d for this turn). %s",
		limit, StopInstruction,
	)
	agg.Continuation = false
	agg.ContinuationLimitReached = true
	return agg
}

// matchingHooks returns the hooks configured for the event, deduplicated
// by command, keeping only those whose matcher accepts the tool name. A
// matcher on an event without a tool name is ignored.
func (r *Runner) matchingHooks(ev Event) []HookConfig {
	seen := make(map[string]bool)
	var matched []HookConfig
	for _, h := range r.source()[ev.Name] {
		if seen[h.Command] {
			continue
		}
		if h.Matcher != "" && ev.Tool != nil {
			re := r.matcher(h)
			if re == nil || !re.MatchString(ev.Tool.Name) {
				continue
			}
		}
		seen[h.Command] = true
		matched = append(matched, h)
	}
	return matched
}

// matcher returns the compiled regex for the hook's matcher, or nil when
// it does not compile.
func (r *Runner) matcher(h HookConfig) *regexp.Regexp {
	r.matchersMu.Lock()
	defer r.matchersMu.Unlock()
	if re, ok := r.matchers[h.Matcher]; ok {
		return re
	}
	re, err := regexp.Compile(h.Matcher)
	if err != nil {
		slog.Warn(
			"Hook matcher failed to compile; skipping hook",
			"matcher", h.Matcher,
			"command", h.Command,
			"error", err,
		)
		re = nil
	}
	r.matchers[h.Matcher] = re
	return re
}

// runOne executes a single hook command and returns its result.
//
// Execution goes through Crush's embedded POSIX shell (shell.Run) so the
// same interpreter, builtins, and coreutils are visible to hooks as to
// the bash tool. BlockFuncs are intentionally omitted: hooks are
// user-authored config that carry the same trust as a shell alias.
//
// A hook that fails to yield after its deadline has passed is abandoned
// after abandonGrace so the caller never blocks longer than
// timeout + abandonGrace. Ownership of the stdout and stderr buffers is
// strictly single-goroutine:
//   - before receiving from `done`, only the goroutine writes to them;
//   - after `done` delivers a value, the goroutine is finished and the
//     outer frame reads them;
//   - on the abandon path, the goroutine may still be writing and the
//     outer frame must not touch them again.
func (r *Runner) runOne(parentCtx context.Context, hook HookConfig, envVars []string, payload []byte) HookResult {
	timeout := hook.TimeoutDuration()
	ctx, cancel := context.WithTimeout(parentCtx, timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runShell(ctx, shell.RunOptions{
			Command: hook.Command,
			Cwd:     r.cwd,
			Env:     envVars,
			Stdin:   bytes.NewReader(payload),
			Stdout:  &stdout,
			Stderr:  &stderr,
		})
	}()

	var err error
	select {
	case err = <-done:
		// Normal path: goroutine has finished, buffers are safe to read.
	case <-ctx.Done():
		select {
		case err = <-done:
			// Interpreter yielded within the grace period; safe to read.
		case <-time.After(abandonGrace):
			slog.Warn(
				"Hook did not yield after cancel; abandoning goroutine",
				"command", hook.Command,
				"timeout", timeout,
			)
			// The goroutine may still be writing to stdout/stderr; do
			// not read either buffer below this point.
			return HookResult{Decision: DecisionNone}
		}
	}

	if shell.IsInterrupt(err) {
		// Distinguish timeout from parent cancellation.
		if parentCtx.Err() != nil {
			slog.Debug("Hook cancelled by parent context", "command", hook.Command)
		} else {
			slog.Warn("Hook timed out", "command", hook.Command, "timeout", timeout)
		}
		return HookResult{Decision: DecisionNone}
	}

	result, hasJSON := parseStdout(stdout.String())
	if err == nil {
		slog.Debug("Hook executed", "command", hook.Command, "decision", result.Decision.String())
		return result
	}

	// Exit 2 blocks the action and 49 halts the turn; stderr is the
	// reason, falling back to any reason the JSON carried. Any other
	// non-zero exit is a non-blocking error, except that, as in Claude
	// Code, a valid JSON object on stdout still counts and the exit code
	// is ignored.
	exitCode := shell.ExitCode(err)
	reason := strings.TrimSpace(stderr.String())
	if reason == "" {
		reason = result.Reason
	}
	switch exitCode {
	case 2:
		if reason == "" {
			reason = "blocked by hook"
		}
		return HookResult{Decision: DecisionDeny, Reason: reason}
	case HaltExitCode:
		if reason == "" {
			reason = "turn halted by hook"
		}
		return HookResult{Decision: DecisionDeny, Halt: true, Reason: reason}
	}
	if hasJSON {
		return result
	}
	slog.Warn(
		"Hook failed with non-blocking error",
		"command", hook.Command,
		"exit_code", exitCode,
		"stderr", strings.TrimSpace(stderr.String()),
		"error", err,
	)
	return HookResult{Decision: DecisionNone}
}
