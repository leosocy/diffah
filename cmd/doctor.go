package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/internal/zstdpatch"
	"github.com/leosocy/diffah/pkg/diff/errs"
)

const (
	statusOK   = "ok"
	statusWarn = "warn"
	statusFail = "fail"
)

type Check interface {
	Name() string
	Run(ctx context.Context) CheckResult
}

type CheckResult struct {
	Status string
	Detail string
	Hint   string
}

func defaultChecks() []Check {
	return []Check{zstdCheck{}}
}

type zstdCheck struct{}

func (zstdCheck) Name() string { return "zstd" }

func (zstdCheck) Run(ctx context.Context) CheckResult {
	ok, detail := zstdpatch.AvailableDetail(ctx)
	if ok {
		return CheckResult{Status: statusOK, Detail: detail}
	}
	return CheckResult{
		Status: statusFail,
		Detail: detail,
		Hint:   "install zstd 1.5+ (brew install zstd / apt install zstd)",
	}
}

func newDoctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Run environment preflight checks.",
		Args:  cobra.NoArgs,
		RunE:  runDoctor,
	}
}

func init() { rootCmd.AddCommand(newDoctorCommand()) }

func runDoctor(cmd *cobra.Command, _ []string) error {
	checks := defaultChecks()
	results := make([]CheckResult, len(checks))
	for i, c := range checks {
		results[i] = c.Run(cmd.Context())
	}

	if outputFormat == outputJSON {
		data := make([]map[string]any, len(checks))
		for i, c := range checks {
			entry := map[string]any{
				"name":   c.Name(),
				"status": results[i].Status,
				"detail": results[i].Detail,
			}
			if results[i].Hint != "" {
				entry["hint"] = results[i].Hint
			}
			data[i] = entry
		}
		if err := writeJSON(cmd.OutOrStdout(), map[string]any{"checks": data}); err != nil {
			return err
		}
	} else {
		renderDoctorText(cmd.OutOrStdout(), checks, results)
	}

	if anyFailed(results) {
		return doctorErr{}
	}
	return nil
}

func renderDoctorText(w io.Writer, checks []Check, results []CheckResult) {
	for i, c := range checks {
		fmt.Fprintf(w, "%-40s %s\n", c.Name(), statusLabel(results[i].Status, results[i].Detail))
		if results[i].Status != statusOK && results[i].Hint != "" {
			fmt.Fprintf(w, "  hint: %s\n", results[i].Hint)
		}
	}
}

func statusLabel(status, detail string) string {
	switch status {
	case statusOK:
		if detail != "" {
			return "ok (" + detail + ")"
		}
		return "ok"
	case statusWarn:
		if detail != "" {
			return statusWarn + " (" + detail + ")"
		}
		return statusWarn
	case statusFail:
		if detail != "" {
			return "fail (" + detail + ")"
		}
		return "fail"
	default:
		if detail != "" {
			return status + " (" + detail + ")"
		}
		return status
	}
}

func anyFailed(rs []CheckResult) bool {
	for _, r := range rs {
		if r.Status == statusFail {
			return true
		}
	}
	return false
}

type doctorErr struct{}

func (doctorErr) Error() string           { return "one or more checks failed" }
func (doctorErr) Category() errs.Category { return errs.CategoryEnvironment }
func (doctorErr) NextAction() string      { return "see failing check for its specific hint" }
