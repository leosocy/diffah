package cmd

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

// verifyConfig carries the runtime inputs consumed by the importer's
// verify hook. An empty PubKeyPath means "no verification requested" —
// the importer must stay byte-identical to today's behavior in that
// case.
type verifyConfig struct {
	PubKeyPath string
	RekorURL   string
}

// verifyConfigBuilder materialises a verifyConfig from flags registered
// on the parent cobra command. The error return is reserved for
// malformed flag combinations (e.g. reserved cosign:// URIs).
type verifyConfigBuilder func() (verifyConfig, error)

// installVerifyFlags registers --verify and --verify-rekor-url on cmd
// and returns a builder for the runtime config. Absent --verify means
// no verification; the caller's importer.Options should carry empty
// strings and the importer skips the verify hook entirely.
func installVerifyFlags(cmd *cobra.Command) verifyConfigBuilder {
	var pubKey, rekor string
	f := cmd.Flags()
	f.StringVar(&pubKey, "verify", "", "public key (PEM) — require signature match")
	f.StringVar(&rekor, "verify-rekor-url", "",
		"fetch Rekor inclusion proof from this transparency log "+
			"(only checked when .rekor.json is present)")

	return func() (verifyConfig, error) {
		if pubKey == "" {
			return verifyConfig{}, nil
		}
		if strings.HasPrefix(pubKey, "cosign://") {
			return verifyConfig{}, &cliErr{
				cat: errs.CategoryUser,
				msg: "cosign:// KMS public-key URIs are reserved but not yet implemented",
			}
		}
		return verifyConfig{PubKeyPath: pubKey, RekorURL: rekor}, nil
	}
}
