package cmd

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestPickHandler_ExplicitJSON(t *testing.T) {
	var buf bytes.Buffer
	h := pickHandler(&buf, "json", &slog.HandlerOptions{Level: slog.LevelInfo}, false)
	logger := slog.New(h)
	logger.Info("hello", "k", "v")
	if !strings.Contains(buf.String(), `"msg":"hello"`) {
		t.Errorf("expected JSON output, got %q", buf.String())
	}
}

func TestPickHandler_ExplicitText(t *testing.T) {
	var buf bytes.Buffer
	h := pickHandler(&buf, "text", &slog.HandlerOptions{Level: slog.LevelInfo}, false)
	logger := slog.New(h)
	logger.Info("hello")
	if strings.Contains(buf.String(), `"msg":"hello"`) {
		t.Errorf("expected text output, got JSON: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "hello") {
		t.Errorf("expected msg=hello, got %q", buf.String())
	}
}

func TestPickHandler_AutoOnTTY_IsText(t *testing.T) {
	t.Setenv("CI", "")
	var buf bytes.Buffer
	h := pickHandler(&buf, "auto", &slog.HandlerOptions{Level: slog.LevelInfo}, true)
	logger := slog.New(h)
	logger.Info("hello")
	if strings.Contains(buf.String(), `"msg":"hello"`) {
		t.Errorf("auto+TTY expected text, got JSON: %q", buf.String())
	}
}

func TestPickHandler_AutoOffTTY_IsJSON(t *testing.T) {
	var buf bytes.Buffer
	h := pickHandler(&buf, "auto", &slog.HandlerOptions{Level: slog.LevelInfo}, false)
	logger := slog.New(h)
	logger.Info("hello")
	if !strings.Contains(buf.String(), `"msg":"hello"`) {
		t.Errorf("auto+non-TTY expected JSON, got %q", buf.String())
	}
}

func TestPickHandler_UnknownFormat_DefaultsJSON(t *testing.T) {
	var buf bytes.Buffer
	h := pickHandler(&buf, "bogus", &slog.HandlerOptions{Level: slog.LevelInfo}, false)
	logger := slog.New(h)
	logger.Info("hello")
	if !strings.Contains(buf.String(), `"msg":"hello"`) {
		t.Errorf("unknown format should default to JSON, got %q", buf.String())
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
		"":      slog.LevelInfo,
	}
	for in, want := range cases {
		if got := parseLevel(in); got != want {
			t.Errorf("parseLevel(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestParseLevel_CaseInsensitive(t *testing.T) {
	if got := parseLevel("DEBUG"); got != slog.LevelDebug {
		t.Errorf("parseLevel(\"DEBUG\") = %s, want %s", got, slog.LevelDebug)
	}
	if got := parseLevel("Warn"); got != slog.LevelWarn {
		t.Errorf("parseLevel(\"Warn\") = %s, want %s", got, slog.LevelWarn)
	}
}

func TestParseLevel_WarningAlias(t *testing.T) {
	if got := parseLevel("warning"); got != slog.LevelWarn {
		t.Errorf("parseLevel(\"warning\") = %s, want %s", got, slog.LevelWarn)
	}
}

func TestInstallLogger_SetsDefault(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	installLogger(&buf, "debug", "json", false, false)
	slog.Default().Info("test", "key", "val")

	if !strings.Contains(buf.String(), `"msg":"test"`) {
		t.Errorf("installLogger should set slog.Default, got %q", buf.String())
	}
}

func TestInstallLogger_QuietOverridesToWarn(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	installLogger(&buf, "debug", "json", true, false)
	slog.Default().Info("should_not_appear")
	slog.Default().Warn("should_appear")

	if strings.Contains(buf.String(), "should_not_appear") {
		t.Errorf("--quiet should suppress info, got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "should_appear") {
		t.Errorf("--quiet should allow warn, got %q", buf.String())
	}
}

func TestInstallLogger_VerboseOverridesToDebug(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	installLogger(&buf, "warn", "json", false, true)
	slog.Default().Debug("debug_msg")

	if !strings.Contains(buf.String(), "debug_msg") {
		t.Errorf("--verbose should enable debug, got %q", buf.String())
	}
}

func TestInstallLogger_QuietWinsOverVerbose(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	installLogger(&buf, "info", "json", true, true)
	slog.Default().Info("info_msg")
	slog.Default().Warn("warn_msg")

	if strings.Contains(buf.String(), "info_msg") {
		t.Errorf("--quiet + --verbose: quiet should win, suppressing info, got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "warn_msg") {
		t.Errorf("--quiet + --verbose: warn should appear, got %q", buf.String())
	}
}

func TestPickHandler_AutoOnTTY_CI_IsJSON(t *testing.T) {
	var buf bytes.Buffer
	t.Setenv("CI", "true")
	h := pickHandler(&buf, "auto", &slog.HandlerOptions{Level: slog.LevelInfo}, true)
	logger := slog.New(h)
	logger.Info("hello")
	if !strings.Contains(buf.String(), `"msg":"hello"`) {
		t.Errorf("auto+TTY+CI=true expected JSON, got %q", buf.String())
	}
}
