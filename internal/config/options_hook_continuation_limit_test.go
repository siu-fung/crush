package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOptionsGetHookContinuationLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		options  *Options
		expected int
	}{
		{"nil options", nil, DefaultHookContinuationLimit},
		{"unset", &Options{}, DefaultHookContinuationLimit},
		{"zero means default", &Options{HookContinuationLimit: ptr(0)}, DefaultHookContinuationLimit},
		{"negative means default", &Options{HookContinuationLimit: ptr(-2)}, DefaultHookContinuationLimit},
		{"configured", &Options{HookContinuationLimit: ptr(3)}, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, tt.options.GetHookContinuationLimit())
		})
	}
}

func TestOptionsHookContinuationLimitFromJSON(t *testing.T) {
	t.Parallel()

	var cfg Config
	require.NoError(t, json.Unmarshal([]byte(`{"options":{"hook_continuation_limit":2}}`), &cfg))
	require.Equal(t, 2, cfg.Options.GetHookContinuationLimit())
}
