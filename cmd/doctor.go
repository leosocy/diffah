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

// defaultChecks returns the five checks that 'diffah doctor' runs in
// order: zstd (binary version), tmpdir (write probe), authfile (lookup
// chain + JSON parse), network (manifest GetManifest under 15s timeout,
// gated by --probe), and config (pkg/config.Validate against
// DefaultPath). probe is the value of the --probe flag (empty = skip
// network probe). buildSysCtx materializes a *types.SystemContext from
// the registry-flag block installed on the doctor command.
func defaultChecks(probe string, buildSysCtx registryContextBuilder) []Check {
	return []Check{
		zstdCheck{},
		tmpdirCheck{},
		authfileCheck{},
		networkCheck{probe: probe, buildSysCtx: buildSysCtx},
		configCheck{},
	}
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
	var probe string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run environment preflight checks.",
		Long: `Run environment preflight checks. Five checks are executed in order:

  zstd      — zstd binary on $PATH and version >= 1.5
  tmpdir    — $TMPDIR (or os.TempDir()) accepts a 1 KiB write
  authfile  — $REGISTRY_AUTH_FILE / $XDG_RUNTIME_DIR / $HOME chain
              resolves to a parseable JSON file with an 'auths' map
  network   — (skipped unless --probe is given) the supplied registry
              reference responds to GetManifest within 15 s
  config    — ~/.diffah/config.yaml (or $DIFFAH_CONFIG) is absent or
              parses cleanly

Exits 3 if any check fails (CategoryEnvironment); warnings do not
change the exit code.`,
		Args: cobra.NoArgs,
	}
	cmd.Flags().StringVar(&probe, "probe", "",
		"image reference (e.g., docker://example.com/foo:tag) for the network check")
	buildSysCtx := installRegistryFlags(cmd)
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		return runDoctor(c, probe, buildSysCtx)
	}
	return cmd
}

func init() { rootCmd.AddCommand(newDoctorCommand()) }

func runDoctor(cmd *cobra.Command, probe string, buildSysCtx registryContextBuilder) error {
	checks := defaultChecks(probe, buildSysCtx)
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
		return errDoctorChecksFailed
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

type doctorChecksFailed struct{}

func (doctorChecksFailed) Error() string           { return "one or more checks failed" }
func (doctorChecksFailed) Category() errs.Category { return errs.CategoryEnvironment }
func (doctorChecksFailed) NextAction() string      { return "see failing check for its specific hint" }

var errDoctorChecksFailed error = doctorChecksFailed{}
