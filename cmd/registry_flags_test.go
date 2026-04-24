package cmd

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func newTestCmd() *cobra.Command {
	c := &cobra.Command{Use: "test"}
	installUsageTemplate(c)
	return c
}

func TestInstallRegistryFlags_RegistersAllFlags(t *testing.T) {
	c := newTestCmd()
	installRegistryFlags(c)

	expected := []string{
		"authfile",
		"creds",
		"username",
		"password",
		"no-creds",
		"registry-token",
		"tls-verify",
		"cert-dir",
		"retry-times",
		"retry-delay",
	}
	for _, name := range expected {
		t.Run(name, func(t *testing.T) {
			require.NotNil(t, c.Flags().Lookup(name), "flag --%s not registered", name)
		})
	}
}

func TestBuild_TranslatesFlagsToSystemContext(t *testing.T) {
	c := newTestCmd()
	build := installRegistryFlags(c)

	require.NoError(t, c.ParseFlags([]string{"--creds", "alice:s3cret", "--retry-times", "5"}))

	sc, retryTimes, _, err := build()
	require.NoError(t, err)
	require.NotNil(t, sc)
	require.NotNil(t, sc.DockerAuthConfig)
	require.Equal(t, "alice", sc.DockerAuthConfig.Username)
	require.Equal(t, "s3cret", sc.DockerAuthConfig.Password)
	require.Equal(t, 5, retryTimes)
}

func TestBuild_UserErrorOnMutualExclusion(t *testing.T) {
	c := newTestCmd()
	build := installRegistryFlags(c)

	require.NoError(t, c.ParseFlags([]string{"--creds", "u:p", "--no-creds"}))

	_, _, _, err := build()
	require.Error(t, err)
	require.Contains(t, err.Error(), "mutually exclusive")
}
