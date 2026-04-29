package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/leosocy/diffah/pkg/config"
)

var configInitForce bool

func newConfigInitCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init [PATH]",
		Short: "Write a template config file.",
		Long: `Writes a template ~/.diffah/config.yaml (or [PATH]) with all nine
fields set to their built-in default values. Refuses to overwrite
an existing file unless --force is given.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runConfigInit,
	}
	cmd.Flags().BoolVar(&configInitForce, "force", false,
		"overwrite an existing file")
	return cmd
}

func runConfigInit(cmd *cobra.Command, args []string) error {
	path := configInitDefaultPath()
	if len(args) == 1 {
		path = args[0]
	}
	if _, err := os.Stat(path); err == nil && !configInitForce {
		return fmt.Errorf("%s already exists; use --force to overwrite", path)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := yaml.Marshal(config.Default())
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", path)
	return nil
}

func configInitDefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.yaml"
	}
	return filepath.Join(home, ".diffah", "config.yaml")
}
