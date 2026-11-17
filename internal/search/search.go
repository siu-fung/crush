// Package search provides full-text search over the messages stored in a
// Crush database.
//
// The messages_fts FTS5 virtual table and the triggers that keep it in
// sync with the messages table are created by the database migrations, so
// this package only needs to build a safe MATCH expression and query the
// index.
package search

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// DefaultLimit is the number of results returned when the caller does not
// ask for a specific limit.
const DefaultLimit = 20

// maxLimit caps how many rows a single query can return.
const maxLimit = 500

// Result is a single message that matched a query. Excerpt is an FTS5
// snippet where the matching terms are wrapped in square brackets.
type Result struct {
	MessageID    string `json:"message_id"`
	SessionID    string `json:"session_id"`
	SessionTitle string `json:"session_title,omitempty"`
	Role         string `json:"role"`
	CreatedAt    int64  `json:"created_at"`
	Excerpt      string `json:"excerpt"`
}

// MatchQuery turns free-form user input into an FTS5 MATCH expression.
// Every whitespace-separated term is quoted so that characters with a
// special meaning in FTS5 syntax (operators, parentheses, column filters)
// are treated as literal text instead of breaking the query.
func MatchQuery(keyword string) string {
	terms := strings.Fields(keyword)
	if len(terms) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.ReplaceAll(term, `"`, `""`)
		quoted = append(quoted, `"`+term+`"`)
	}
	return strings.Join(quoted, " ")
}

// Search returns messages matching keyword, best matches first. A limit of
// zero or less falls back to [DefaultLimit].
func Search(ctx context.Context, conn *sql.DB, keyword string, limit int) ([]Result, error) {
	match := MatchQuery(keyword)
	if match == "" {
		return nil, errors.New("empty search query")
	}
	switch {
	case limit <= 0:
		limit = DefaultLimit
	case limit > maxLimit:
		limit = maxLimit
	}

	rows, err := conn.QueryContext(ctx, searchQuery, match, limit)
	if err != nil {
		return nil, fmt.Errorf("searching messages: %w", err)
	}
	defer rows.Close()

	results := make([]Result, 0, limit)
	for rows.Next() {
		var (
			result  Result
			excerpt sql.NullString
		)
		if err := rows.Scan(
			&result.MessageID,
			&result.SessionID,
			&result.Role,
			&result.CreatedAt,
			&result.SessionTitle,
			&excerpt,
		); err != nil {
			return nil, fmt.Errorf("scanning search result: %w", err)
		}
		result.Excerpt = excerpt.String
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading search results: %w", err)
	}
	return results, nil
}

// searchQuery joins the FTS index back to the message and session rows.
// snippet() takes the real table name and the zero-based index of the
// indexed "content" column (0: message_id, 1: session_id, 2: role,
// 3: content), so the FTS table is deliberately not aliased.
const searchQuery = `
SELECT
    messages_fts.message_id,
    messages_fts.session_id,
    messages_fts.role,
    COALESCE(m.created_at, 0),
    COALESCE(s.title, ''),
    snippet(messages_fts, 3, '[', ']', '…', 12)
FROM messages_fts
LEFT JOIN messages m ON m.id = messages_fts.message_id
LEFT JOIN sessions s ON s.id = messages_fts.session_id
WHERE messages_fts MATCH ?
ORDER BY rank
LIMIT ?`
