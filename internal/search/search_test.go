package search

import (
	"context"
	"testing"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/stretchr/testify/require"
)

func TestMatchQuery(t *testing.T) {
	t.Parallel()

	require.Equal(t, `"fox" "dog"`, MatchQuery("  fox\tdog "))
	require.Equal(t, `"""hi"""`, MatchQuery(`"hi"`))
	require.Equal(t, `"foo" "OR" "bar"`, MatchQuery("foo OR bar"))
	require.Empty(t, MatchQuery("   "))
}

func TestSearchAcrossMessageLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dataDir := t.TempDir()

	conn, err := db.Connect(ctx, dataDir)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
	})

	_, err = conn.ExecContext(ctx,
		`INSERT INTO sessions (id, title, updated_at, created_at) VALUES (?, ?, ?, ?)`,
		"session-1", "Search tests", 100, 100,
	)
	require.NoError(t, err)

	_, err = conn.ExecContext(ctx,
		`INSERT INTO messages (id, session_id, role, parts, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"message-1", "session-1", "user",
		`[{"type":"text","data":{"text":"A quick unicorn jumps over the lazy dog"}}]`,
		100, 100,
	)
	require.NoError(t, err)

	// The insert trigger must index the new message.
	results, err := Search(ctx, conn, "unicorn", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "message-1", results[0].MessageID)
	require.Equal(t, "session-1", results[0].SessionID)
	require.Equal(t, "user", results[0].Role)
	require.Equal(t, "Search tests", results[0].SessionTitle)
	require.Equal(t, int64(100), results[0].CreatedAt)
	require.Contains(t, results[0].Excerpt, "unicorn")

	// Updating a user message replaces its index entry.
	_, err = conn.ExecContext(ctx,
		`UPDATE messages SET parts = ? WHERE id = ?`,
		`[{"type":"text","data":{"text":"A quick penguin takes a nap"}}]`,
		"message-1",
	)
	require.NoError(t, err)

	results, err = Search(ctx, conn, "unicorn", 10)
	require.NoError(t, err)
	require.Empty(t, results)

	results, err = Search(ctx, conn, "penguin", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)

	// Tool output is part of the indexed message content.
	_, err = conn.ExecContext(ctx,
		`INSERT INTO messages (id, session_id, role, parts, created_at, updated_at, finished_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"message-2", "session-1", "tool",
		`[{"type":"tool_result","data":{"tool_call_id":"call-1","name":"bash","content":"gorilla found in output"}}]`,
		101, 101, 101,
	)
	require.NoError(t, err)

	results, err = Search(ctx, conn, "gorilla", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "message-2", results[0].MessageID)

	// Deleting a message removes it from the index.
	_, err = conn.ExecContext(ctx, `DELETE FROM messages WHERE id = ?`, "message-1")
	require.NoError(t, err)

	results, err = Search(ctx, conn, "penguin", 10)
	require.NoError(t, err)
	require.Empty(t, results)
}

func TestSearchIndexesFinishedAssistantMessages(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dataDir := t.TempDir()

	conn, err := db.Connect(ctx, dataDir)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
	})

	_, err = conn.ExecContext(ctx,
		`INSERT INTO sessions (id, title, updated_at, created_at) VALUES (?, ?, ?, ?)`,
		"session-1", "Assistant", 1, 1,
	)
	require.NoError(t, err)

	// An assistant message is created empty and then streamed in through
	// updates; only the terminal update (finished_at set) reindexes it.
	_, err = conn.ExecContext(ctx,
		`INSERT INTO messages (id, session_id, role, parts, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"message-1", "session-1", "assistant", `[]`, 1, 1,
	)
	require.NoError(t, err)

	_, err = conn.ExecContext(ctx,
		`UPDATE messages SET parts = ?, updated_at = ? WHERE id = ?`,
		`[{"type":"text","data":{"text":"streaming walrus"}}]`,
		2, "message-1",
	)
	require.NoError(t, err)

	results, err := Search(ctx, conn, "walrus", 10)
	require.NoError(t, err)
	require.Empty(t, results, "mid-stream updates must not be reindexed")

	_, err = conn.ExecContext(ctx,
		`UPDATE messages SET parts = ?, finished_at = ? WHERE id = ?`,
		`[{"type":"text","data":{"text":"final walrus answer"}}]`,
		3, "message-1",
	)
	require.NoError(t, err)

	results, err = Search(ctx, conn, "walrus", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "message-1", results[0].MessageID)
}

func TestBackfillIndexesExistingMessages(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dataDir := t.TempDir()

	conn, err := db.Connect(ctx, dataDir)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
	})

	_, err = conn.ExecContext(ctx,
		`INSERT INTO sessions (id, title, updated_at, created_at) VALUES (?, ?, ?, ?)`,
		"session-1", "Backfill", 1, 1,
	)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx,
		`INSERT INTO messages (id, session_id, role, parts, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"message-1", "session-1", "user",
		`[{"type":"text","data":{"text":"existing armadillo message"}}]`,
		1, 1,
	)
	require.NoError(t, err)

	// Simulate a database whose messages predate the index: empty the FTS
	// table and run the same backfill statement the migration uses.
	_, err = conn.ExecContext(ctx, `DELETE FROM messages_fts`)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx,
		`INSERT INTO messages_fts (message_id, session_id, role, content)
		 SELECT message_id, session_id, role, content FROM message_search_text`,
	)
	require.NoError(t, err)

	results, err := Search(ctx, conn, "armadillo", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "message-1", results[0].MessageID)
}

func TestSearchRespectsLimitAndRejectsEmptyQuery(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dataDir := t.TempDir()

	conn, err := db.Connect(ctx, dataDir)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
	})

	_, err = Search(ctx, conn, "   ", 10)
	require.Error(t, err)

	_, err = conn.ExecContext(ctx,
		`INSERT INTO sessions (id, title, updated_at, created_at) VALUES (?, ?, ?, ?)`,
		"session-1", "Limits", 1, 1,
	)
	require.NoError(t, err)
	for _, id := range []string{"message-1", "message-2", "message-3"} {
		_, err = conn.ExecContext(ctx,
			`INSERT INTO messages (id, session_id, role, parts, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			id, "session-1", "user",
			`[{"type":"text","data":{"text":"shared keyword"}}]`,
			1, 1,
		)
		require.NoError(t, err)
	}

	results, err := Search(ctx, conn, "keyword", 2)
	require.NoError(t, err)
	require.Len(t, results, 2)
}
