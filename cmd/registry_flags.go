package cmd

import (
	"time"

	"github.com/spf13/cobra"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/diff/errs"
)

// registryContextBuilder materialises a *types.SystemContext plus retry
// parameters from the flags registered on the parent cobra command. It
// should be called from the subcommand's RunE after cobra has parsed
// argv, so cmd.Flags().Changed("tls-verify") reflects the user's intent.
type registryContextBuilder func() (*types.SystemContext, int, time.Duration, error)

// installRegistryFlags registers the registry & transport flag block on
// cmd and returns a build closure that produces a *types.SystemContext
// plus retry parameters. Installed on every subcommand that speaks to a
// registry.
func installRegistryFlags(cmd *cobra.Command) registryContextBuilder {
	flags := &imageio.SystemContextFlags{}
	tlsVerify := true
	f := cmd.Flags()
	f.StringVar(&flags.AuthFile, "authfile", "", "path to authentication file")
	f.StringVar(&flags.Creds, "creds", "", "inline credentials USER[:PASS]")
	f.StringVar(&flags.Username, "username", "", "registry username")
	f.StringVar(&flags.Password, "password", "", "registry password")
	f.BoolVar(&flags.NoCreds, "no-creds", false, "access the registry anonymously")
	f.StringVar(&flags.RegistryToken, "registry-token", "", "bearer token")
	f.BoolVar(&tlsVerify, "tls-verify", true, "require HTTPS and verify certificates")
	f.StringVar(&flags.CertDir, "cert-dir", "", "directory of client certificates")
	f.IntVar(&flags.RetryTimes, "retry-times", 3, "retry count for transient failures")
	f.DurationVar(&flags.RetryDelay, "retry-delay", 0, "fixed inter-retry delay (default: exponential)")

	return func() (*types.SystemContext, int, time.Duration, error) {
		if cmd.Flags().Changed("tls-verify") {
			flags.TLSVerify = &tlsVerify
		}
		sc, err := imageio.BuildSystemContext(*flags)
		if err != nil {
			return nil, 0, 0, &cliErr{cat: errs.CategoryUser, msg: err.Error()}
		}
		return sc, flags.RetryTimes, flags.RetryDelay, nil
	}
}
