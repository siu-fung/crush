package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

func newQueueForTest(entries []QueueEntry) *Queue {
	s := styles.CharmtonePantera()
	com := &common.Common{Styles: &s}
	return NewQueue(com, entries)
}

func newTimelineForTest(entries []TimelineEntry) *Timeline {
	s := styles.CharmtonePantera()
	com := &common.Common{Styles: &s}
	return NewTimeline(com, entries)
}

func TestQueue_RemoveSelectedPrompt(t *testing.T) {
	t.Parallel()

	q := newQueueForTest([]QueueEntry{
		{Index: 0, Prompt: "first"},
		{Index: 1, Prompt: "second"},
		{Index: 2, Prompt: "third"},
	})

	require.Nil(t, q.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown}))
	action := q.HandleMsg(tea.KeyPressMsg{Code: 'x'})

	removed, ok := action.(ActionRemoveQueuedPrompt)
	require.True(t, ok)
	require.Equal(t, 1, removed.Index, "the action must name the queue slot, not the list row")
	require.Equal(t, "second", removed.Prompt, "the raw prompt travels with the index")
	require.Equal(t, []QueueEntry{
		{Index: 0, Prompt: "first"},
		{Index: 2, Prompt: "third"},
	}, q.entries())
}

func TestQueue_ClearAll(t *testing.T) {
	t.Parallel()

	q := newQueueForTest([]QueueEntry{
		{Index: 0, Prompt: "first"},
		{Index: 1, Prompt: "second"},
	})

	action := q.HandleMsg(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	require.IsType(t, ActionClearQueue{}, action)
	require.Empty(t, q.entries())
}

func TestQueue_SetEntriesKeepsSelectionInRange(t *testing.T) {
	t.Parallel()

	q := newQueueForTest([]QueueEntry{
		{Index: 0, Prompt: "first"},
		{Index: 1, Prompt: "second"},
	})
	q.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown})

	// The agent drained an entry underneath the panel.
	q.SetEntries([]QueueEntry{{Index: 0, Prompt: "first"}})

	require.Equal(t, []QueueEntry{{Index: 0, Prompt: "first"}}, q.entries())
	require.Equal(t, 0, q.list.Selected())
}

func TestQueue_DisplayPrefersLabel(t *testing.T) {
	t.Parallel()

	// Subagent reports queue an encoded prompt but show a description, and
	// the dismissal still has to name the raw text.
	entry := QueueEntry{Index: 0, Prompt: "<report payload>", Label: "Subagent report: build"}
	require.Equal(t, "Subagent report: build", entry.Display())
	require.Equal(t, "plain prompt", QueueEntry{Prompt: "plain\n prompt"}.Display())
}

func TestQueue_EmptyHasNoSelection(t *testing.T) {
	t.Parallel()

	q := newQueueForTest(nil)
	require.Empty(t, q.entries())
	require.Nil(t, q.HandleMsg(tea.KeyPressMsg{Code: 'x'}), "removing from an empty queue is a no-op")
	require.Nil(t, q.HandleMsg(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}), "clearing an empty queue is a no-op")
}

func TestTimeline_SelectReturnsRewind(t *testing.T) {
	t.Parallel()

	tl := newTimelineForTest([]TimelineEntry{
		{MessageID: "m2", Prompt: "newest", CreatedAt: 200},
		{MessageID: "m1", Prompt: "older", CreatedAt: 100},
	})

	require.Nil(t, tl.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown}))
	action := tl.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})

	rewind, ok := action.(ActionRewind)
	require.True(t, ok)
	require.Equal(t, "m1", rewind.MessageID)
	require.Equal(t, "older", rewind.Prompt)
}

func TestTimeline_CloseKey(t *testing.T) {
	t.Parallel()

	tl := newTimelineForTest([]TimelineEntry{{MessageID: "m1", Prompt: "only"}})
	require.IsType(t, ActionClose{}, tl.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape}))
}
