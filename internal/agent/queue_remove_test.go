package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSessionAgent_RemoveQueuedPrompt verifies that removing one queued
// prompt keeps the rest in order, that an out-of-range index or an
// unknown session removes nothing, and that dropping the last entry
// clears the queue map entry.
func TestSessionAgent_RemoveQueuedPrompt(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	a := NewSessionAgent(SessionAgentOptions{
		Models:   noModels,
		Sessions: env.sessions,
		Messages: env.messages,
	}).(*sessionAgent)

	const sessionID = "remove-session"
	a.messageQueue.Set(sessionID, []SessionAgentCall{
		{SessionID: sessionID, Prompt: "first"},
		{SessionID: sessionID, Prompt: "second"},
		{SessionID: sessionID, Prompt: "third"},
	})

	require.True(t, a.RemoveQueuedPrompt(sessionID, 1, ""))

	left, ok := a.messageQueue.Get(sessionID)
	require.True(t, ok)
	require.Len(t, left, 2)
	require.Equal(t, "first", left[0].Prompt)
	require.Equal(t, "third", left[1].Prompt)

	require.False(t, a.RemoveQueuedPrompt(sessionID, 5, ""),
		"an out-of-range index must not remove anything")
	require.False(t, a.RemoveQueuedPrompt("other-session", 0, ""),
		"an unknown session must not remove anything")

	require.True(t, a.RemoveQueuedPrompt(sessionID, 0, ""))
	require.True(t, a.RemoveQueuedPrompt(sessionID, 0, ""))

	_, ok = a.messageQueue.Get(sessionID)
	require.False(t, ok, "removing the last entry must drop the queue map entry")
}

// TestSessionAgent_RemoveQueuedPromptStaleIndex covers the panel case: the
// queue drained a head entry since the caller snapshotted it, so every
// position shifted down. The prompt text must win over the stale position.
func TestSessionAgent_RemoveQueuedPromptStaleIndex(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	a := NewSessionAgent(SessionAgentOptions{
		Models:   noModels,
		Sessions: env.sessions,
		Messages: env.messages,
	}).(*sessionAgent)

	const sessionID = "stale-session"
	// The caller last saw [first, second, third]; by now "first" is
	// running, so the queue is [second, third] and the caller's index 1
	// points at "third" instead of "second".
	a.messageQueue.Set(sessionID, []SessionAgentCall{
		{SessionID: sessionID, Prompt: "second"},
		{SessionID: sessionID, Prompt: "third"},
	})

	require.True(t, a.RemoveQueuedPrompt(sessionID, 1, "second"))

	left, ok := a.messageQueue.Get(sessionID)
	require.True(t, ok)
	require.Len(t, left, 1)
	require.Equal(t, "third", left[0].Prompt, "the intended prompt is gone, not its neighbour")
}

// TestSessionAgent_RemoveQueuedPromptUnknownText reports that a prompt which
// already left the queue removes nothing, instead of falling back to the
// position and dropping a different entry.
func TestSessionAgent_RemoveQueuedPromptUnknownText(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	a := NewSessionAgent(SessionAgentOptions{
		Models:   noModels,
		Sessions: env.sessions,
		Messages: env.messages,
	}).(*sessionAgent)

	const sessionID = "gone-session"
	a.messageQueue.Set(sessionID, []SessionAgentCall{
		{SessionID: sessionID, Prompt: "first"},
		{SessionID: sessionID, Prompt: "second"},
	})

	require.False(t, a.RemoveQueuedPrompt(sessionID, 0, "already ran"))

	left, ok := a.messageQueue.Get(sessionID)
	require.True(t, ok)
	require.Len(t, left, 2, "a prompt with no match must not remove anything")
}
