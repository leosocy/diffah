package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

func requireArgs(verb string, argNames []string, example string) cobra.PositionalArgs {
	want := len(argNames)
	usage := "diffah " + verb + " " + strings.Join(argNames, " ")
	argList := strings.Join(argNames, ", ")
	return func(_ *cobra.Command, args []string) error {
		if len(args) == want {
			return nil
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "'%s' requires %d arguments (%s), got %d.\n\n",
			verb, want, argList, len(args))
		fmt.Fprintf(&sb, "Usage:\n  %s\n\n", usage)
		fmt.Fprintf(&sb, "Example:\n  %s\n\n", example)
		fmt.Fprintf(&sb, "Run 'diffah %s --help' for more examples.", verb)
		return &cliErr{cat: errs.CategoryUser, msg: sb.String(), hint: "run 'diffah " + verb + " --help' for details"}
	}
}
