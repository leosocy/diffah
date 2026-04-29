package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/leosocy/diffah/pkg/config"
)

func newConfigShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the resolved config.",
		Long: `Resolves the same lookup chain a real run uses
($DIFFAH_CONFIG > ~/.diffah/config.yaml > defaults) and prints
the resulting Config struct. Useful for debugging
"why is intra-layer=off in CI?".

--format=json prints JSON instead of YAML.`,
		Args: cobra.NoArgs,
		RunE: runConfigShow,
	}
}

func runConfigShow(cmd *cobra.Command, _ []string) error {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	if outputFormat == outputJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(cfg)
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	fmt.Fprint(w, string(out))
	return nil
}
