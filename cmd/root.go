package cmd

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff/errs"
	"github.com/leosocy/diffah/pkg/progress"
)

var version = "dev"

const outputJSON = "json"

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
	h := pickHandler(slogWriter, currentLogFormat(), opts, tty)
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

func currentLogFormat() string {
	return logFormat
}

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVar(&logLevel, "log-level", "info", "log level: debug|info|warn|error (env: DIFFAH_LOG_LEVEL)")
	pf.StringVar(&logFormat, "log-format", "auto", "log format: auto|text|json (env: DIFFAH_LOG_FORMAT)")
	pf.BoolVar(&quiet, "quiet", false, "suppress info logs and progress bars (level=warn)")
	pf.BoolVar(&verbose, "verbose", false, "enable debug logs (level=debug)")
	pf.StringVar(&progressMode, "progress", "auto", "progress output: auto|bars|lines|off")
	pf.StringVar(&outputFormat, "output", "text",
		"output format: text|json (applies to inspect/dry-run/doctor and error rendering)")

	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		lvl := logLevel
		if v := os.Getenv("DIFFAH_LOG_LEVEL"); v != "" && !cmd.Flags().Changed("log-level") {
			lvl = v
		}
		logFmt := logFormat
		if v := os.Getenv("DIFFAH_LOG_FORMAT"); v != "" && !cmd.Flags().Changed("log-format") {
			logFmt = v
		}
		installLogger(cmd.ErrOrStderr(), lvl, logFmt, quiet, verbose)
		return nil
	}
}
