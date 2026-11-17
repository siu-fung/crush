package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// buildAgentPrompts renders the coder, task, and plan prompts against a
// config loaded from cfgJSON, the same way the coordinator builds them.
// The config is hermetic: it reads nothing from the developer's own
// Crush config.
func buildAgentPrompts(t *testing.T, workingDir, cfgJSON string) map[string]string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(home, ".config", "crush"))
	t.Setenv("CRUSH_GLOBAL_DATA", filepath.Join(home, ".local", "share", "crush"))

	require.NoError(t, os.WriteFile(filepath.Join(workingDir, "crush.json"), []byte(cfgJSON), 0o644))
	store, err := config.Init(t.Context(), workingDir, "", false)
	require.NoError(t, err)

	builders := map[string]func(...prompt.Option) (*prompt.Prompt, error){
		config.AgentCoder: coderPrompt,
		config.AgentTask:  taskPrompt,
		config.AgentPlan:  planPrompt,
	}
	rendered := make(map[string]string, len(builders))
	for name, newPrompt := range builders {
		p, err := newPrompt(prompt.WithWorkingDir(workingDir))
		require.NoError(t, err)
		systemPrompt, err := p.Build(context.Background(), "mock", "mock-model", store)
		require.NoError(t, err)
		rendered[name] = systemPrompt
	}
	return rendered
}

// agentConfigJSON is a minimal provider config so config.Init succeeds
// without touching the user's real providers.
func agentConfigJSON(options string) string {
	return `{
  "options": {` + options + `, "disable_default_providers": true, "disable_provider_auto_update": true},
  "providers": {"mock": {"id": "mock", "name": "Mock", "type": "openai",
    "base_url": "http://127.0.0.1:9/v1", "api_key": "test-key",
    "models": [{"id": "mock-model", "name": "Mock", "context_window": 8192, "default_max_tokens": 128}]}},
  "models": {"large": {"provider": "mock", "model": "mock-model"},
             "small": {"provider": "mock", "model": "mock-model"}}
}`
}

func TestCustomSystemPromptReachesEveryAgent(t *testing.T) {
	workingDir := t.TempDir()
	rendered := buildAgentPrompts(t, workingDir, agentConfigJSON(
		`"system_prompt": "Always answer in Spanish."`,
	))

	for name, systemPrompt := range rendered {
		require.Contains(t, systemPrompt, "<custom_instructions>",
			"%s prompt must carry the custom instructions", name)
		require.Contains(t, systemPrompt, "\nAlways answer in Spanish.\n",
			"%s prompt must carry the configured text", name)
		require.Contains(t, systemPrompt, "</custom_instructions>",
			"%s prompt must close the custom instructions section", name)
	}
}

func TestCustomSystemPromptFile(t *testing.T) {
	workingDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workingDir, "prompts"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(workingDir, "prompts", "extra.md"),
		[]byte("\nUse tabs, never spaces.\n"),
		0o644,
	))

	rendered := buildAgentPrompts(t, workingDir, agentConfigJSON(
		`"system_prompt": "Inline first.", "system_prompt_file": "prompts/extra.md"`,
	))
	systemPrompt := rendered[config.AgentCoder]

	inline := strings.Index(systemPrompt, "Inline first.")
	fromFile := strings.Index(systemPrompt, "Use tabs, never spaces.")
	require.NotEqual(t, -1, inline)
	require.NotEqual(t, -1, fromFile, "the relative system_prompt_file must resolve against the working directory")
	require.Less(t, inline, fromFile, "inline text must come before the file's contents")
}

func TestCustomSystemPromptFileMissingIsAnError(t *testing.T) {
	workingDir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(workingDir, "global"))
	t.Setenv("CRUSH_GLOBAL_DATA", filepath.Join(workingDir, "global-data"))
	require.NoError(t, os.WriteFile(
		filepath.Join(workingDir, "crush.json"),
		[]byte(agentConfigJSON(`"system_prompt_file": "does-not-exist.md"`)),
		0o644,
	))
	store, err := config.Init(t.Context(), workingDir, "", false)
	require.NoError(t, err)

	p, err := coderPrompt()
	require.NoError(t, err)
	_, err = p.Build(context.Background(), "mock", "mock-model", store)
	require.ErrorContains(t, err, "system_prompt_file")
}

func TestNoCustomSystemPromptLeavesPromptUnchanged(t *testing.T) {
	workingDir := t.TempDir()
	rendered := buildAgentPrompts(t, workingDir, agentConfigJSON(`"debug": false`))

	for name, systemPrompt := range rendered {
		require.NotContains(t, systemPrompt, "custom_instructions",
			"%s prompt must not mention custom instructions when none are configured", name)
	}
	// Templates must not leak the conditional's surrounding blank line.
	require.True(t, strings.HasSuffix(rendered[config.AgentTask], "</env>\n"))
}
