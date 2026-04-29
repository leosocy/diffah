package cmd

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/config"
	"github.com/leosocy/diffah/pkg/diff/errs"
	"github.com/leosocy/diffah/pkg/progress"
)

var version = "dev"

const (
	outputText = "text"
	outputJSON = "json"
)

var (
	logLevel     string
	logFormat    string
	quiet        bool
	verbose      bool
	progressMode string
	outputFormat string
)

var rootCmd = &cobra.Command{
	Use:   "diffah",
	Short: "Produce and apply portable container-image layer-diff archives.",
	Long: `diffah computes a layer-level diff between two container images,
packages the new layers into a portable archive, and reconstructs the
full target image from any baseline source on the consuming side.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute(stderr io.Writer) int {
	return classifyAndExit(stderr, rootCmd.Execute(), outputFormat)
}

func classifyAndExit(w io.Writer, err error, format string) int {
	if err == nil {
		return 0
	}
	cat, hint := errs.Classify(err)
	renderError(w, cat, err, hint, format)
	return cat.ExitCode()
}

func renderError(w io.Writer, cat errs.Category, err error, hint, format string) {
	if format == outputJSON {
		_ = writeJSONError(w, cat.String(), err.Error(), hint)
		return
	}
	fmt.Fprintf(w, "diffah: %s: %s\n", cat, err.Error())
	if hint != "" {
		fmt.Fprintf(w, "  hint: %s\n", hint)
	}
}

func newProgressReporter(w io.Writer) progress.Reporter {
	if quiet {
		return progress.NewDiscard()
	}
	tty := progress.IsTTY(w)
	switch progressMode {
	case "off":
		return progress.NewDiscard()
	case "lines":
		return progress.NewLine(w)
	case "bars":
		r := progress.NewBars(w)
		rewireSlogToBars(r, tty)
		return r
	default:
		r := progress.NewAuto(w)
		rewireSlogToBars(r, tty)
		return r
	}
}

func rewireSlogToBars(r progress.Reporter, tty bool) {
	sw, ok := r.(progress.SlogWriterProvider)
	if !ok {
		return
	}
	slogWriter := sw.SlogWriter()
	opts := &slog.HandlerOptions{Level: parseLevel(currentLogLevel())}
	h := pickHandler(slogWriter, logFormat, opts, tty)
	slog.SetDefault(slog.New(h))
}

func currentLogLevel() string {
	if verbose {
		return "debug"
	}
	if quiet {
		return "warn"
	}
	return logLevel
}

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVar(&logLevel, "log-level", "info", "log level: debug|info|warn|error (env: DIFFAH_LOG_LEVEL)")
	pf.StringVar(&logFormat, "log-format", "auto", "log format: auto|text|json (env: DIFFAH_LOG_FORMAT)")
	pf.BoolVarP(&quiet, "quiet", "q", false, "suppress info logs and progress bars (level=warn)")
	pf.BoolVarP(&verbose, "verbose", "v", false, "enable debug logs (level=debug)")
	pf.StringVar(&progressMode, "progress", "auto", "progress output: auto|bars|lines|off")
	pf.StringVarP(&outputFormat, "format", "o", outputText,
		"rendering format: text|json (applies to inspect/dry-run/doctor and error output)")

	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		switch outputFormat {
		case outputText, outputJSON:
		default:
			return &cliErr{
				cat:  errs.CategoryUser,
				msg:  fmt.Sprintf("invalid --format %q (valid: text, json)", outputFormat),
				hint: "pass --format text or --format json",
			}
		}
		lvl := logLevel
		if v := os.Getenv("DIFFAH_LOG_LEVEL"); v != "" && !cmd.Flags().Changed("log-level") {
			lvl = v
		}
		logFmt := logFormat
		if v := os.Getenv("DIFFAH_LOG_FORMAT"); v != "" && !cmd.Flags().Changed("log-format") {
			logFmt = v
		}
		// The 'config' subtree and 'doctor' must run even when the resolved
		// config file is malformed — that's how operators diagnose the
		// breakage. Skip the persistent load+apply for those commands.
		if !isExemptFromConfigLoad(cmd) {
			if err := loadAndApplyConfig(cmd); err != nil {
				return err
			}
		}
		installLogger(cmd.ErrOrStderr(), lvl, logFmt, quiet, verbose)
		return nil
	}
}

// loadAndApplyConfig loads the config from DefaultPath and applies it to
// the command's flag set. CLI-explicit flags already win because ApplyTo
// only writes when flag.Changed is false.
func loadAndApplyConfig(cmd *cobra.Command) error {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return err
	}
	return config.ApplyTo(cmd.Flags(), cfg)
}

// isExemptFromConfigLoad reports whether cmd should skip the persistent
// config load+apply step. Two reasons to exempt:
//
//  1. The 'config' subtree (show/init/validate) MUST run even when the
//     resolved config file is malformed — that's how operators diagnose
//     the breakage.
//
//  2. The 'doctor' command is the diagnostic escape hatch and must be
//     able to report a malformed config structurally rather than
//     hard-failing in PersistentPreRunE before runDoctor fires.
func isExemptFromConfigLoad(cmd *cobra.Command) bool {
	if cmd.Name() == "doctor" {
		return true
	}
	for c := cmd; c != nil; c = c.Parent() {
		if c.Name() == "config" {
			return true
		}
	}
	return false
}
