package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/workspace"
)

// timelineLoadedMsg carries the user prompts a rewind can return to.
type timelineLoadedMsg struct {
	forSession string
	entries    []dialog.TimelineEntry
}

// rewoundMsg reports a completed rewind: the transcript was truncated and
// prompt belongs back in the editor.
type rewoundMsg struct {
	forSession string
	prompt     string
	removed    int
}

// queueChangedMsg reports the outcome of a queue edit made from the queue
// panel.
type queueChangedMsg struct {
	forSession string
	// cleared reports a clear-all rather than a single removal.
	cleared bool
	// removed is false when the requested removal found nothing, which
	// means the agent drained the entry in the meantime.
	removed bool
}

// openTimelineCmd loads the session's user prompts off the Update goroutine
// and opens the rewind dialog once they arrive. The query is bounded to the
// most recent prompts, so it stays cheap even on a long session.
func (m *UI) openTimelineCmd() tea.Cmd {
	if !m.hasSession() {
		return nil
	}
	ws := m.com.Workspace
	sessionID := m.session.ID
	return func() tea.Msg {
		msgs, err := ws.ListUserMessages(context.Background(), sessionID)
		if err != nil {
			return util.NewErrorMsg(err)
		}
		entries := make([]dialog.TimelineEntry, 0, len(msgs))
		for _, msg := range msgs {
			if !msg.IsPrompt() {
				continue
			}
			text := strings.TrimSpace(msg.Content().Text)
			if text == "" {
				continue
			}
			entries = append(entries, dialog.TimelineEntry{
				MessageID: msg.ID,
				Prompt:    text,
				CreatedAt: msg.CreatedAt,
			})
		}
		return timelineLoadedMsg{forSession: sessionID, entries: entries}
	}
}

// applyTimelineLoaded opens the rewound-conversation picker.
func (m *UI) applyTimelineLoaded(msg timelineLoadedMsg) []tea.Cmd {
	if !m.hasSession() || m.session.ID != msg.forSession {
		// The user moved to another session while the prompts loaded.
		return nil
	}
	if len(msg.entries) == 0 {
		return []tea.Cmd{util.ReportInfo("No prompts to rewind to yet.")}
	}
	if m.dialog.ContainsDialog(dialog.TimelineID) {
		m.dialog.BringToFront(dialog.TimelineID)
		return nil
	}
	m.dialog.OpenDialog(dialog.NewTimeline(m.com, msg.entries))
	return nil
}

// rewindSessionCmd truncates the session transcript at messageID off the
// Update goroutine: that message and everything after it are deleted.
func (m *UI) rewindSessionCmd(messageID, prompt string) tea.Cmd {
	ws := m.com.Workspace
	sessionID := m.session.ID
	return func() tea.Msg {
		removed, err := ws.RewindSession(context.Background(), sessionID, messageID)
		if err != nil {
			if errors.Is(err, workspace.ErrSessionBusy) {
				// The run is still appending messages, so the truncation
				// point would move underneath it.
				return util.ReportWarn("Agent is busy; wait for it to finish before rewinding.")
			}
			return util.NewErrorMsg(err)
		}
		return rewoundMsg{forSession: sessionID, prompt: prompt, removed: removed}
	}
}

// applyRewound restores the rewound prompt into the editor and reloads the
// now-truncated transcript.
func (m *UI) applyRewound(msg rewoundMsg) []tea.Cmd {
	if !m.hasSession() || m.session.ID != msg.forSession {
		return nil
	}
	var cmds []tea.Cmd
	prevHeight := m.textarea.Height()
	m.textarea.SetValue(msg.prompt)
	m.textarea.CursorEnd()
	if cmd := m.handleTextareaHeightChange(prevHeight); cmd != nil {
		cmds = append(cmds, cmd)
	}
	// The point of a rewind is to edit and resend the prompt, so the
	// editor takes focus along with the text.
	m.focus = uiFocusEditor
	m.chat.Blur()
	cmds = append(cmds, m.textarea.Focus())
	cmds = append(cmds, util.ReportInfo(fmt.Sprintf(
		"Rewound conversation (%d message(s) removed); prompt restored in the editor.",
		msg.removed,
	)))
	cmds = append(cmds, m.loadSession(msg.forSession))
	return cmds
}

// openQueueDialog opens the prompt queue panel, refreshing it from the
// workspace in the background.
func (m *UI) openQueueDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.QueueID) {
		m.dialog.BringToFront(dialog.QueueID)
		return nil
	}
	m.dialog.OpenDialog(dialog.NewQueue(m.com, m.queueEntries()))
	return m.dispatchPromptQueueRefresh()
}

// queueEntries turns the cached queued prompts into panel entries. The
// cache is event-driven and refreshed on open, so it can trail the agent by
// at most one refresh. Prompt stays raw so a dismissal can still name its
// entry after the queue drains; Label is only what the row shows.
func (m *UI) queueEntries() []dialog.QueueEntry {
	entries := make([]dialog.QueueEntry, 0, len(m.promptQueueItems))
	for i, prompt := range m.promptQueueItems {
		entries = append(entries, dialog.QueueEntry{
			Index:  i,
			Prompt: prompt,
			Label:  queueEntryLabel(prompt),
		})
	}
	return entries
}

// queueEntryLabel renders a queued prompt for the panel. Subagent reports
// travel through the queue as encoded prompts, so they are described rather
// than dumped raw.
func queueEntryLabel(prompt string) string {
	if report, ok := message.ParseSubAgentReportPrompt(prompt); ok {
		return "Subagent report: " + report.Label
	}
	return singleLine(prompt)
}

// removeQueuedPromptCmd drops one queued prompt off the Update goroutine.
// The prompt text travels with the index so the agent removes the entry the
// panel meant even if the queue drained a head entry in the meantime.
func (m *UI) removeQueuedPromptCmd(index int, prompt string) tea.Cmd {
	ws := m.com.Workspace
	sessionID := m.session.ID
	return func() tea.Msg {
		removed := ws.AgentRemoveQueuedPrompt(sessionID, index, prompt)
		return queueChangedMsg{forSession: sessionID, removed: removed}
	}
}

// clearQueueCmd drops every queued prompt off the Update goroutine.
func (m *UI) clearQueueCmd() tea.Cmd {
	ws := m.com.Workspace
	sessionID := m.session.ID
	return func() tea.Msg {
		ws.AgentClearQueue(sessionID)
		return queueChangedMsg{forSession: sessionID, cleared: true, removed: true}
	}
}

// applyQueueChanged updates the cached queue state after a panel edit and
// resyncs it with the agent.
func (m *UI) applyQueueChanged(msg queueChangedMsg) []tea.Cmd {
	if !m.hasSession() || m.session.ID != msg.forSession {
		return nil
	}
	if !msg.cleared && !msg.removed {
		cmds := []tea.Cmd{util.ReportWarn("That prompt already left the queue.")}
		if cmd := m.dispatchPromptQueueRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return cmds
	}
	if msg.cleared {
		m.promptQueue = 0
		m.promptQueueItems = nil
	} else {
		m.promptQueue = max(0, m.promptQueue-1)
	}
	m.promptQueueCheckedAt = time.Now()
	// Bump the generation so a fetch started before this edit cannot land
	// and repopulate the entries we just dropped.
	m.invalidatePromptQueue()
	m.updateLayoutAndSize()
	if cmd := m.dispatchPromptQueueRefresh(); cmd != nil {
		return []tea.Cmd{cmd}
	}
	return nil
}
