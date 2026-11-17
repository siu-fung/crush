package cmd

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/search"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/spf13/cobra"
)

var searchCmd = &cobra.Command{
	Use:   "search <keyword>",
	Short: "Search session messages",
	Long: `Full-text search across the messages of every session in the current project.

The search index is the SQLite FTS5 table maintained alongside the session
database, so matches include prompts, assistant replies, tool calls, and
command output.`,
	Example: `
# Search for a keyword
crush search "flaky test"

# Return machine-readable results
crush search --json "flaky test"

# Limit the number of matches
crush search --limit 5 "FTS5"`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOutput, _ := cmd.Flags().GetBool("json")
		limit, _ := cmd.Flags().GetInt("limit")
		keyword := strings.Join(args, " ")

		ctx, conn, cleanup, err := searchSetup(cmd)
		if err != nil {
			return err
		}
		defer cleanup()

		results, err := search.Search(ctx, conn, keyword, limit)
		if err != nil {
			return err
		}

		if jsonOutput {
			return writeSearchJSON(cmd.OutOrStdout(), keyword, results)
		}
		writeSearchResults(cmd.OutOrStdout(), results)
		return nil
	},
}

func init() {
	searchCmd.Flags().Bool("json", false, "Output as JSON")
	searchCmd.Flags().IntP("limit", "l", search.DefaultLimit, "Maximum number of matches")
}

// searchSetup opens the current project's session database. It mirrors the
// lightweight config + database path used by the session commands instead of
// building a full app, so searching never starts agents, LSPs, or MCP
// servers.
func searchSetup(cmd *cobra.Command) (context.Context, *sql.DB, func(), error) {
	dataDir, _ := cmd.Flags().GetString("data-dir")
	ctx := cmd.Context()

	cwd, err := ResolveCwd(cmd)
	if err != nil {
		return nil, nil, nil, err
	}

	cfg, err := config.Init(ctx, cwd, dataDir, false)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to initialize config: %w", err)
	}
	if dataDir == "" {
		dataDir = cfg.Config().Options.DataDirectory
	}

	conn, err := db.Connect(ctx, dataDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to connect to database: %w", err)
	}
	return ctx, conn, func() { _ = conn.Close() }, nil
}

// searchJSONOutput is the machine-readable shape of `crush search --json`.
type searchJSONOutput struct {
	Query   string          `json:"query"`
	Count   int             `json:"count"`
	Results []search.Result `json:"results"`
}

func writeSearchJSON(w io.Writer, keyword string, results []search.Result) error {
	out := searchJSONOutput{
		Query:   keyword,
		Count:   len(results),
		Results: results,
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}

func writeSearchResults(w io.Writer, results []search.Result) {
	if len(results) == 0 {
		_, _ = fmt.Fprintln(w, "No matches found.")
		return
	}
	for _, result := range results {
		title := oneLine(result.SessionTitle)
		if title == "" {
			title = session.HashID(result.SessionID)
		}
		_, _ = fmt.Fprintf(w, "%-16s  %s (%s)\n", formatSearchTime(result.CreatedAt), title, session.HashID(result.SessionID)[:12])
		_, _ = fmt.Fprintf(w, "  [%s] %s\n", result.Role, oneLine(result.Excerpt))
	}
}

func formatSearchTime(unix int64) string {
	if unix == 0 {
		return "unknown time"
	}
	return time.Unix(unix, 0).Local().Format("2006-01-02 15:04")
}

// oneLine collapses newlines so a multi-line message still renders as a
// single search hit.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ExecuteSearch runs the search command with the provided arguments. It is
// the entry point of the standalone crush-search binary, which is a thin
// wrapper around `crush search`.
func ExecuteSearch(args []string) {
	rootCmd.SetArgs(append([]string{"search"}, args...))
	Execute()
}
