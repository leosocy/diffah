package cmd

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/config"
)

// TestConfigDefaults_MatchCobraFlagDefaults locks the contract that
// pkg/config.Default() agrees with the flag defaults installed by each
// cobra command. ApplyTo writes config values onto cobra flags whenever
// flag.Changed is false, so any divergence here means a user without a
// config file would see the config-side default instead of the flag's
// default — silently regressing pre-Phase-5.2 behavior.
func TestConfigDefaults_MatchCobraFlagDefaults(t *testing.T) {
	d := config.Default()

	cases := []struct {
		flag   string
		expect string
		newCmd func() *cobra.Command
	}{
		{"platform", d.Platform, newDiffCommand},
		{"intra-layer", d.IntraLayer, newDiffCommand},
		{"authfile", d.Authfile, newApplyCommand},
		{"retry-times", "3", newApplyCommand},
		{"retry-delay", "0s", newApplyCommand},
		{"zstd-level", "22", newDiffCommand},
		{"zstd-window-log", d.ZstdWindowLog, newDiffCommand},
		{"workers", "8", newDiffCommand},
		{"candidates", "3", newDiffCommand},
	}

	for _, c := range cases {
		t.Run(c.flag, func(t *testing.T) {
			cmd := c.newCmd()
			f := cmd.Flags().Lookup(c.flag)
			require.NotNilf(t, f, "command %s does not register --%s", cmd.Use, c.flag)
			require.Equalf(t, c.expect, f.DefValue,
				"flag --%s default %q must match config.Default() %q to preserve no-config behavior",
				c.flag, f.DefValue, c.expect)
		})
	}

	// Cross-check the literal expectations against d's int / duration
	// fields so a future change to Default() can't make the table lie.
	require.Equal(t, 3, d.RetryTimes)
	require.Equal(t, "0s", d.RetryDelay.String())
	require.Equal(t, 22, d.ZstdLevel)
	require.Equal(t, 8, d.Workers)
	require.Equal(t, 3, d.Candidates)
}
