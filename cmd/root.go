package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

var version = "dev"

var (
	logLevel  string
	logFormat string
	quiet     bool
	verbose   bool
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
	err := rootCmd.Execute()
	if err == nil {
		return 0
	}
	cat, hint := errs.Classify(err)
	if cat == errs.CategoryInternal {
		cat = errs.CategoryUser
		hint = "run 'diffah --help' for usage"
	}
	renderError(stderr, cat, err, hint, outputFormatFlag())
	return cat.ExitCode()
}

func renderError(w io.Writer, cat errs.Category, err error, hint, format string) {
	if format == "json" {
		payload := struct {
			SchemaVersion int `json:"schema_version"`
			Error         struct {
				Category   string `json:"category"`
				Message    string `json:"message"`
				NextAction string `json:"next_action,omitempty"`
			} `json:"error"`
		}{SchemaVersion: 1}
		payload.Error.Category = cat.String()
		payload.Error.Message = err.Error()
		payload.Error.NextAction = hint
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(payload)
		return
	}
	fmt.Fprintf(w, "diffah: %s: %s\n", cat, err.Error())
	if hint != "" {
		fmt.Fprintf(w, "  hint: %s\n", hint)
	}
}

func ClassifyExitCode(err error) int {
	if err == nil {
		return 0
	}
	cat, _ := errs.Classify(err)
	return cat.ExitCode()
}

func RenderError(w io.Writer, err error, format string) {
	if err == nil {
		return
	}
	cat, hint := errs.Classify(err)
	renderError(w, cat, err, hint, format)
}

func outputFormatFlag() string {
	if f := rootCmd.PersistentFlags().Lookup("output"); f != nil {
		return f.Value.String()
	}
	return "text"
}

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVar(&logLevel, "log-level", "info", "log level: debug|info|warn|error (env: DIFFAH_LOG_LEVEL)")
	pf.StringVar(&logFormat, "log-format", "auto", "log format: auto|text|json (env: DIFFAH_LOG_FORMAT)")
	pf.BoolVar(&quiet, "quiet", false, "suppress info logs and progress bars (level=warn)")
	pf.BoolVar(&verbose, "verbose", false, "enable debug logs (level=debug)")

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
