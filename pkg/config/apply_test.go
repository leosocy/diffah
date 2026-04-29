package config

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

func newFlagSet() *pflag.FlagSet {
	f := pflag.NewFlagSet("test", pflag.ContinueOnError)
	f.String("platform", "linux/amd64", "")
	f.String("intra-layer", "auto", "")
	f.String("authfile", "", "")
	f.Int("retry-times", 0, "")
	f.Duration("retry-delay", 0, "")
	f.Int("zstd-level", 22, "")
	f.String("zstd-window-log", "auto", "")
	f.Int("workers", 8, "")
	f.Int("candidates", 3, "")
	return f
}

func TestApplyTo_OverridesDefaultWhenFlagNotChanged(t *testing.T) {
	f := newFlagSet()
	cfg := Default()
	cfg.Platform = "linux/arm64"
	cfg.Workers = 4

	require.NoError(t, ApplyTo(f, cfg))

	got, _ := f.GetString("platform")
	require.Equal(t, "linux/arm64", got)
	gotW, _ := f.GetInt("workers")
	require.Equal(t, 4, gotW)
}

func TestApplyTo_PreservesExplicitFlag(t *testing.T) {
	f := newFlagSet()
	require.NoError(t, f.Parse([]string{"--platform=linux/explicit"}))

	cfg := Default()
	cfg.Platform = "linux/from-config" // should NOT win

	require.NoError(t, ApplyTo(f, cfg))

	got, _ := f.GetString("platform")
	require.Equal(t, "linux/explicit", got)
}

func TestApplyTo_IgnoresFlagsNotPresent(t *testing.T) {
	// A FlagSet that only has 'platform' (e.g., the diff command
	// before retry-times was wired) must not error when ApplyTo
	// encounters Config fields with no matching flag.
	f := pflag.NewFlagSet("partial", pflag.ContinueOnError)
	f.String("platform", "linux/amd64", "")

	cfg := Default()
	cfg.Platform = "linux/arm64"
	cfg.RetryTimes = 99 // no flag for this in `f` — must be silently skipped

	require.NoError(t, ApplyTo(f, cfg))

	got, _ := f.GetString("platform")
	require.Equal(t, "linux/arm64", got)
}
