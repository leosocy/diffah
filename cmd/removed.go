package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

func init() {
	rootCmd.AddCommand(newRemovedExport())
	rootCmd.AddCommand(newRemovedImport())
}

func newRemovedExport() *cobra.Command {
	return &cobra.Command{
		Use:                "export",
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(*cobra.Command, []string) error {
			return removedErr("export", []removedReplacement{
				{verb: "diff", args: "BASELINE-IMAGE TARGET-IMAGE DELTA-OUT", note: "single-image delta"},
				{verb: "bundle", args: "BUNDLE-SPEC DELTA-OUT", note: "multi-image bundle via spec file"},
			})
		},
	}
}

func newRemovedImport() *cobra.Command {
	return &cobra.Command{
		Use:                "import",
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(*cobra.Command, []string) error {
			return removedErr("import", []removedReplacement{
				{verb: "apply", args: "DELTA-IN BASELINE-IMAGE TARGET-IMAGE", note: "single-image apply"},
				{verb: "unbundle", args: "DELTA-IN BASELINE-SPEC OUTPUT-SPEC", note: "multi-image unbundle via spec file"},
			})
		},
	}
}

type removedReplacement struct {
	verb string
	args string
	note string
}

func removedErr(old string, replacements []removedReplacement) error {
	var verbWidth, argsWidth int
	for _, r := range replacements {
		if len(r.verb) > verbWidth {
			verbWidth = len(r.verb)
		}
		if len(r.args) > argsWidth {
			argsWidth = len(r.args)
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "unknown command '%s'. This command was removed in the CLI redesign.\n\n", old)
	sb.WriteString("Did you mean one of:\n")
	for _, r := range replacements {
		fmt.Fprintf(&sb, "  diffah %-*s %-*s  # %s\n", verbWidth, r.verb, argsWidth, r.args, r.note)
	}
	sb.WriteString("\nRun 'diffah --help' for the full command list.")
	return &cliErr{cat: errs.CategoryUser, msg: sb.String(), hint: "run 'diffah --help' for the full command list"}
}
