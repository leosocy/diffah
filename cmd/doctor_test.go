package cmd_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/cmd"
)

func TestDoctor_JSONShape(t *testing.T) {
	var stdout bytes.Buffer
	rc := cmd.Run(&stdout, nil, "doctor", "--format", "json")

	var env struct {
		SchemaVersion int `json:"schema_version"`
		Data          struct {
			Checks []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
				Detail string `json:"detail"`
				Hint   string `json:"hint"`
			} `json:"checks"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &env))
	require.Equal(t, 1, env.SchemaVersion)
	require.NotEmpty(t, env.Data.Checks, "expected at least one check")

	names := make([]string, 0, len(env.Data.Checks))
	for _, c := range env.Data.Checks {
		names = append(names, c.Name)
	}
	require.True(t, containsStr(names, "zstd"), "expected zstd check among %v", names)

	for _, c := range env.Data.Checks {
		require.Contains(t, []string{"ok", "warn", "fail"}, c.Status,
			"check %q has invalid status", c.Name)
	}

	if rc != 0 {
		require.Equal(t, 3, rc, "doctor should exit 3 (environment) on failure")
	}
}

func TestDoctor_TextOutput(t *testing.T) {
	var stdout bytes.Buffer
	cmd.Run(&stdout, nil, "doctor")

	out := stdout.String()
	require.Contains(t, out, "zstd", "text output should mention zstd check")
}

func TestDoctor_TextOutput_StatusLabels(t *testing.T) {
	var stdout bytes.Buffer
	cmd.Run(&stdout, nil, "doctor")

	out := stdout.String()
	ok := strings.Contains(out, "ok") || strings.Contains(out, "fail")
	require.True(t, ok, "text output should contain ok or fail status, got: %q", out)
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
