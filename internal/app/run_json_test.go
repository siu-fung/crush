package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunNonInteractiveJSON(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if err := streamReply(w, scriptedDelta{Role: "assistant", Content: "json walrus"}, "stop"); err != nil {
			t.Errorf("stream reply: %v", err)
		}
	}))
	t.Cleanup(provider.Close)

	app := newRunTestApp(t, provider.URL)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	result, err := app.RunNonInteractiveJSON(ctx, "hello", "", "", "", "", false)
	require.NoError(t, err)
	require.NotEmpty(t, result.SessionID)
	require.NotEmpty(t, result.SessionTitle)
	require.Equal(t, "mock", result.Provider)
	require.Equal(t, "mock-model", result.Model)
	require.Equal(t, "json walrus", result.Text)
	require.Equal(t, "stop", result.FinishReason)
	require.GreaterOrEqual(t, result.Steps, 1)

	// The returned value must be directly serializable as the CLI payload.
	data, err := json.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(data), `"text":"json walrus"`)
}
