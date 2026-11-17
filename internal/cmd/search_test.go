package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/stretchr/testify/require"
)

func TestSearchCommandJSON(t *testing.T) {
	t.Setenv("CRUSH_GLOBAL_CONFIG", t.TempDir())
	t.Setenv("CRUSH_GLOBAL_DATA", t.TempDir())
	t.Setenv("CRUSH_DISABLE_PROVIDER_AUTO_UPDATE", "1")

	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	gitInit := exec.CommandContext(t.Context(), "git", "init", "-q")
	gitInit.Dir = root
	require.NoError(t, gitInit.Run())

	dataDir := filepath.Join(root, ".crush")
	require.NoError(t, os.Mkdir(dataDir, 0o700))
	t.Chdir(root)

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	_, err = conn.ExecContext(t.Context(),
		`INSERT INTO sessions (id, title, updated_at, created_at) VALUES (?, ?, ?, ?)`,
		"session-1", "CLI search", 1, 1,
	)
	require.NoError(t, err)
	_, err = conn.ExecContext(t.Context(),
		`INSERT INTO messages (id, session_id, role, parts, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"message-1", "session-1", "user",
		`[{"type":"text","data":{"text":"a command line unicorn"}}]`,
		1, 1,
	)
	require.NoError(t, err)
	require.NoError(t, db.Release(dataDir))

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"search", "--json", "unicorn"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	require.NoError(t, rootCmd.ExecuteContext(t.Context()))

	var payload searchJSONOutput
	require.NoError(t, json.Unmarshal(out.Bytes(), &payload))
	require.Equal(t, "unicorn", payload.Query)
	require.Equal(t, 1, payload.Count)
	require.Len(t, payload.Results, 1)
	require.Equal(t, "message-1", payload.Results[0].MessageID)
}

func TestRunJSONRejectsClientServer(t *testing.T) {
	t.Setenv("CRUSH_CLIENT_SERVER", "1")
	t.Cleanup(func() {
		_ = runCmd.Flags().Set("json", "false")
		rootCmd.SetArgs(nil)
	})

	rootCmd.SetArgs([]string{"run", "--json", "hello"})
	err := rootCmd.ExecuteContext(t.Context())
	require.ErrorContains(t, err, "CRUSH_CLIENT_SERVER")
}
