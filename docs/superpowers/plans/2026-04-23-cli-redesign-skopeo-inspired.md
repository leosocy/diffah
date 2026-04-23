# CLI Redesign (skopeo-inspired) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `export` / `import` + composite flag surface with skopeo-style `diff` / `apply` / `bundle` / `unbundle` subcommands, strict transport-prefixed image references, and guided error messages.

**Architecture:** CLI-only refactor. Library packages (`pkg/diff`, `pkg/exporter`, `pkg/importer`, `pkg/progress`) are **not modified**. New `cmd/*.go` files add subcommands, a transport parser, a custom help template, and an arg-count validator. Old `cmd/export.go` / `cmd/import.go` are deleted; invoking the old verbs hits a redirection trap.

**Tech Stack:** Go 1.21+, `github.com/spf13/cobra`, `github.com/stretchr/testify`, existing `pkg/diff/errs` taxonomy, existing `go.podman.io/image/v5` via `internal/imageio`.

**Spec:** `docs/superpowers/specs/2026-04-23-cli-redesign-skopeo-inspired-design.md`

---

## Pre-flight: branch already created

Brainstorming session already switched to branch `spec/cli-redesign-skopeo-inspired` and committed the design doc (`7c9e2b9`). All subsequent commits in this plan land on that same branch.

Verify before starting:

```bash
git branch --show-current   # expect: spec/cli-redesign-skopeo-inspired
git log --oneline -1        # expect: 7c9e2b9 docs(cli): spec skopeo-inspired CLI redesign
```

---

## Stage 1 — Shared CLI infrastructure

Build the reusable primitives that every new subcommand will consume: the image-reference parser, the Arguments-section help template, the argument-count validator, and the removed-command trap.

### Task 1.1: Image reference parser

**Files:**
- Create: `cmd/transport.go`
- Test: `cmd/transport_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// cmd/transport_test.go
package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseImageRef_AcceptsSupportedTransports(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantT   string
		wantP   string
	}{
		{"docker-archive", "docker-archive:/tmp/x.tar", "docker-archive", "/tmp/x.tar"},
		{"oci-archive", "oci-archive:/tmp/y.tar", "oci-archive", "/tmp/y.tar"},
		{"docker-archive relative", "docker-archive:x.tar", "docker-archive", "x.tar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := ParseImageRef("BASELINE-IMAGE", tc.raw)
			require.NoError(t, err)
			require.Equal(t, tc.wantT, ref.Transport)
			require.Equal(t, tc.wantP, ref.Path)
		})
	}
}

func TestParseImageRef_MissingTransport(t *testing.T) {
	_, err := ParseImageRef("BASELINE-IMAGE", "/tmp/old.tar")
	require.Error(t, err)
	msg := err.Error()
	require.Contains(t, msg, "missing transport prefix")
	require.Contains(t, msg, "BASELINE-IMAGE")
	require.Contains(t, msg, `"/tmp/old.tar"`)
	require.Contains(t, msg, "docker-archive:PATH")
	require.Contains(t, msg, "oci-archive:PATH")
	require.Contains(t, msg, "Did you mean:  docker-archive:/tmp/old.tar")
}

func TestParseImageRef_MissingTransportNoHintForNonTarExt(t *testing.T) {
	_, err := ParseImageRef("TARGET-IMAGE", "/srv/layout")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "Did you mean:")
}

func TestParseImageRef_ReservedTransports(t *testing.T) {
	cases := []string{
		"docker://registry/img:v1",
		"oci:/tmp/layout",
		"dir:/tmp/raw",
		"docker-daemon:img:v1",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, err := ParseImageRef("BASELINE-IMAGE", raw)
			require.Error(t, err)
			msg := err.Error()
			require.Contains(t, msg, "is reserved but not yet implemented")
			require.Contains(t, msg, "docker-archive:PATH")
			require.Contains(t, msg, "oci-archive:PATH")
		})
	}
}

func TestParseImageRef_EmptyPath(t *testing.T) {
	_, err := ParseImageRef("BASELINE-IMAGE", "docker-archive:")
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "empty path") ||
		strings.Contains(err.Error(), "empty"))
}

func TestParseImageRef_EmptyString(t *testing.T) {
	_, err := ParseImageRef("BASELINE-IMAGE", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing transport prefix")
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./cmd/ -run TestParseImageRef -v
```

Expected: all tests fail with `undefined: ParseImageRef`.

- [ ] **Step 3: Implement the parser**

```go
// cmd/transport.go
package cmd

import (
	"fmt"
	"strings"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

// ImageRef is the parsed form of a user-supplied `transport:path` image
// reference. Only archive transports are currently supported for input
// positionals; future library work will enable the reserved ones.
type ImageRef struct {
	Transport string
	Path      string
}

var supportedInputTransports = map[string]bool{
	"docker-archive": true,
	"oci-archive":    true,
}

var reservedInputTransports = map[string]bool{
	"oci":                true,
	"dir":                true,
	"docker":             true, // from docker://
	"docker-daemon":      true,
	"containers-storage": true,
	"ostree":             true,
	"sif":                true,
	"tarball":            true,
}

// ParseImageRef validates a raw image reference and returns its parsed form.
// argName is surfaced in error messages so the user knows which positional
// was rejected (e.g. "BASELINE-IMAGE"). Errors are classified user (exit 2).
func ParseImageRef(argName, raw string) (ImageRef, error) {
	prefix, rest, ok := splitTransport(raw)
	if !ok {
		return ImageRef{}, newMissingTransportErr(argName, raw)
	}
	if reservedInputTransports[prefix] {
		return ImageRef{}, newReservedTransportErr(argName, prefix)
	}
	if !supportedInputTransports[prefix] {
		return ImageRef{}, newMissingTransportErr(argName, raw)
	}
	if rest == "" {
		return ImageRef{}, &cliErr{
			cat: errs.CategoryUser,
			msg: fmt.Sprintf("transport %q for %s has empty path: %q", prefix, argName, raw),
		}
	}
	return ImageRef{Transport: prefix, Path: rest}, nil
}

// splitTransport splits "prefix:rest" with special-casing for "prefix://rest".
func splitTransport(raw string) (prefix, rest string, ok bool) {
	colon := strings.Index(raw, ":")
	if colon <= 0 {
		return "", "", false
	}
	prefix = raw[:colon]
	rest = raw[colon+1:]
	rest = strings.TrimPrefix(rest, "//")
	return prefix, rest, true
}

// --- error types ---

type cliErr struct {
	cat     errs.Category
	msg     string
	hint    string
}

func (e *cliErr) Error() string              { return e.msg }
func (e *cliErr) Category() errs.Category    { return e.cat }
func (e *cliErr) NextAction() string         { return e.hint }

func newMissingTransportErr(argName, raw string) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "missing transport prefix for %s: %q\n\n", argName, raw)
	sb.WriteString("Image references require a transport prefix. Supported transports:\n")
	sb.WriteString("  docker-archive:PATH     # Docker tar archive (docker save)\n")
	sb.WriteString("  oci-archive:PATH        # OCI tar archive (skopeo copy ... oci-archive:...)\n")
	if hint := didYouMean(raw); hint != "" {
		fmt.Fprintf(&sb, "\nDid you mean:  %s\n", hint)
	}
	return &cliErr{cat: errs.CategoryUser, msg: sb.String()}
}

func newReservedTransportErr(argName, prefix string) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "transport %q (in %s) is reserved but not yet implemented.\n\n", prefix, argName)
	sb.WriteString("Supported transports in this version:\n")
	sb.WriteString("  docker-archive:PATH     # Docker tar archive (docker save)\n")
	sb.WriteString("  oci-archive:PATH        # OCI tar archive (skopeo copy ... oci-archive:...)\n\n")
	sb.WriteString("Tracking: see CHANGELOG / roadmap for expanded transport support.\n")
	return &cliErr{cat: errs.CategoryUser, msg: sb.String()}
}

func didYouMean(raw string) string {
	lower := strings.ToLower(raw)
	if strings.HasSuffix(lower, ".tar") ||
		strings.HasSuffix(lower, ".tgz") ||
		strings.HasSuffix(lower, ".tar.gz") {
		return "docker-archive:" + raw
	}
	return ""
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./cmd/ -run TestParseImageRef -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/transport.go cmd/transport_test.go
git commit -m "feat(cmd): add transport-prefixed image reference parser

Supports docker-archive: and oci-archive:; rejects reserved transports
(oci:, dir:, docker://, docker-daemon:) with guidance; emits 'Did you
mean' hint for bare paths that look like tar archives."
```

---

### Task 1.2: Argument-count validator

**Files:**
- Create: `cmd/args.go`
- Test: `cmd/args_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// cmd/args_test.go
package cmd

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestRequireArgs_TooFew(t *testing.T) {
	cmd := &cobra.Command{Use: "diff"}
	validator := requireArgs("diff",
		[]string{"BASELINE-IMAGE", "TARGET-IMAGE", "DELTA-OUT"},
		"diffah diff docker-archive:/tmp/old.tar docker-archive:/tmp/new.tar delta.tar",
	)
	err := validator(cmd, []string{"docker-archive:/tmp/x.tar"})
	require.Error(t, err)
	msg := err.Error()
	require.Contains(t, msg, "'diff' requires 3 arguments")
	require.Contains(t, msg, "BASELINE-IMAGE, TARGET-IMAGE, DELTA-OUT")
	require.Contains(t, msg, "got 1")
	require.Contains(t, msg, "Usage:\n  diffah diff BASELINE-IMAGE TARGET-IMAGE DELTA-OUT")
	require.Contains(t, msg, "Example:\n  diffah diff docker-archive:")
	require.Contains(t, msg, "Run 'diffah diff --help'")
}

func TestRequireArgs_TooMany(t *testing.T) {
	cmd := &cobra.Command{Use: "apply"}
	validator := requireArgs("apply",
		[]string{"DELTA-IN", "BASELINE-IMAGE", "TARGET-OUT"},
		"diffah apply delta.tar docker-archive:/tmp/old.tar docker-archive:/tmp/out.tar",
	)
	err := validator(cmd, []string{"a", "b", "c", "d"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "got 4")
}

func TestRequireArgs_ExactCount(t *testing.T) {
	cmd := &cobra.Command{Use: "diff"}
	validator := requireArgs("diff",
		[]string{"BASELINE-IMAGE", "TARGET-IMAGE", "DELTA-OUT"},
		"diffah diff ...",
	)
	err := validator(cmd, []string{"a", "b", "c"})
	require.NoError(t, err)
}

func TestRequireArgs_ErrorIsCategoryUser(t *testing.T) {
	cmd := &cobra.Command{Use: "diff"}
	validator := requireArgs("diff",
		[]string{"BASELINE-IMAGE", "TARGET-IMAGE", "DELTA-OUT"},
		"diffah diff ...",
	)
	err := validator(cmd, []string{})
	require.Error(t, err)
	// Force classification via fake root to exercise end-to-end.
	var buf bytes.Buffer
	code := classifyAndExit(&buf, err, "text")
	require.Equal(t, 2, code)
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./cmd/ -run TestRequireArgs -v
```

Expected: FAIL with `undefined: requireArgs`.

- [ ] **Step 3: Implement**

```go
// cmd/args.go
package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

// requireArgs returns a cobra.PositionalArgs validator that enforces exactly
// len(argNames) positional arguments and, on mismatch, returns a multi-line
// error with a usage line, a concrete example, and a pointer to --help.
// verb is the subcommand name used in the error template (e.g. "diff").
// example is the full command line shown under "Example:".
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
		return &cliErr{cat: errs.CategoryUser, msg: sb.String()}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./cmd/ -run TestRequireArgs -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/args.go cmd/args_test.go
git commit -m "feat(cmd): add requireArgs validator with usage+example guidance

Replaces cobra.ExactArgs(N) for new subcommands with a validator that
produces multi-line errors including usage line, copy-paste example,
and --help pointer. Classified as user error (exit 2)."
```

---

### Task 1.3: Arguments-section usage template

**Files:**
- Create: `cmd/help.go`
- Test: `cmd/help_test.go`

- [ ] **Step 1: Write the failing test**

```go
// cmd/help_test.go
package cmd

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestInstallUsageTemplate_RendersArgumentsSection(t *testing.T) {
	cmd := &cobra.Command{
		Use:   "diff BASELINE-IMAGE TARGET-IMAGE DELTA-OUT",
		Short: "Compute a single-image delta.",
		Annotations: map[string]string{
			"arguments": "  BASELINE-IMAGE   old image to diff against (transport:path)\n" +
				"  TARGET-IMAGE     new image to diff against (transport:path)\n" +
				"  DELTA-OUT        filesystem path to write the delta archive",
		},
		Example: "  diffah diff docker-archive:/tmp/old.tar docker-archive:/tmp/new.tar delta.tar",
		Run:     func(*cobra.Command, []string) {},
	}
	installUsageTemplate(cmd)

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	require.NoError(t, cmd.Usage())

	out := buf.String()
	require.Contains(t, out, "Arguments:")
	require.Contains(t, out, "BASELINE-IMAGE   old image to diff against")
	require.Contains(t, out, "TARGET-IMAGE     new image to diff against")
	require.Contains(t, out, "DELTA-OUT        filesystem path to write")
	require.Contains(t, out, "Examples:")
	// Ordering: Arguments: must come before Examples:
	require.Less(t, bytes.Index(buf.Bytes(), []byte("Arguments:")),
		bytes.Index(buf.Bytes(), []byte("Examples:")))
}

func TestInstallUsageTemplate_OmitsArgumentsWhenAnnotationMissing(t *testing.T) {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version.",
		Run:   func(*cobra.Command, []string) {},
	}
	installUsageTemplate(cmd)

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	require.NoError(t, cmd.Usage())
	require.NotContains(t, buf.String(), "Arguments:")
}
```

- [ ] **Step 2: Run to verify fail**

```bash
go test ./cmd/ -run TestInstallUsageTemplate -v
```

Expected: FAIL with `undefined: installUsageTemplate`.

- [ ] **Step 3: Implement**

```go
// cmd/help.go
package cmd

import "github.com/spf13/cobra"

// usageTemplateWithArguments extends cobra's default usage template with an
// optional "Arguments:" section, rendered from the `arguments` annotation
// on the command. If the annotation is absent the section is omitted.
const usageTemplateWithArguments = `Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if (index .Annotations "arguments")}}

Arguments:
{{index .Annotations "arguments"}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}

Available Commands:{{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`

// installUsageTemplate attaches the Arguments-aware template to cmd. Call
// once per subcommand that sets an `arguments` annotation.
func installUsageTemplate(cmd *cobra.Command) {
	cmd.SetUsageTemplate(usageTemplateWithArguments)
}
```

- [ ] **Step 4: Run to verify pass**

```bash
go test ./cmd/ -run TestInstallUsageTemplate -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/help.go cmd/help_test.go
git commit -m "feat(cmd): install usage template with Arguments section

Adds an 'Arguments:' block to --help output, read from the cobra
command's 'arguments' annotation. Placed between Aliases: and
Examples:. Subcommands opt in by calling installUsageTemplate."
```

---

### Task 1.4: Removed-command trap

**Files:**
- Create: `cmd/removed.go`
- Test: `cmd/removed_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// cmd/removed_test.go
package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRemovedCommand_ExportRedirects(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "export", "--pair", "app=v1.tar,v2.tar", "bundle.tar")
	require.Equal(t, 2, code)
	out := stderr.String()
	require.Contains(t, out, "unknown command 'export'")
	require.Contains(t, out, "was removed in the CLI redesign")
	require.Contains(t, out, "diffah diff")
	require.Contains(t, out, "BASELINE-IMAGE TARGET-IMAGE DELTA-OUT")
	require.Contains(t, out, "diffah bundle")
	require.Contains(t, out, "BUNDLE-SPEC")
	require.Contains(t, out, "diffah --help")
}

func TestRemovedCommand_ImportRedirects(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "import", "--baseline", "default=v1.tar", "--delta", "d.tar", "o.tar")
	require.Equal(t, 2, code)
	out := stderr.String()
	require.Contains(t, out, "unknown command 'import'")
	require.Contains(t, out, "diffah apply")
	require.Contains(t, out, "DELTA-IN BASELINE-IMAGE TARGET-OUT")
	require.Contains(t, out, "diffah unbundle")
	require.Contains(t, out, "BASELINE-SPEC")
}
```

- [ ] **Step 2: Run to verify fail**

```bash
go test ./cmd/ -run TestRemovedCommand -v
```

Expected: FAIL (`unknown command` from cobra, not our custom message).

- [ ] **Step 3: Implement**

```go
// cmd/removed.go
package cmd

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

func init() {
	rootCmd.AddCommand(newRemovedExport())
	rootCmd.AddCommand(newRemovedImport())
}

func newRemovedExport() *cobra.Command {
	c := &cobra.Command{
		Use:                "export",
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(*cobra.Command, []string) error {
			return removedErr("export", []removedReplacement{
				{verb: "diff", args: "BASELINE-IMAGE TARGET-IMAGE DELTA-OUT", note: "single-image delta"},
				{verb: "bundle", args: "BUNDLE-SPEC    DELTA-OUT", note: "multi-image bundle via spec file"},
			})
		},
	}
	return c
}

func newRemovedImport() *cobra.Command {
	c := &cobra.Command{
		Use:                "import",
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(*cobra.Command, []string) error {
			return removedErr("import", []removedReplacement{
				{verb: "apply", args: "DELTA-IN BASELINE-IMAGE TARGET-OUT", note: "single-image apply"},
				{verb: "unbundle", args: "DELTA-IN BASELINE-SPEC  OUTPUT-DIR", note: "multi-image unbundle via spec file"},
			})
		},
	}
	return c
}

type removedReplacement struct {
	verb string
	args string
	note string
}

func removedErr(old string, replacements []removedReplacement) error {
	var sb strings.Builder
	sb.WriteString("unknown command '")
	sb.WriteString(old)
	sb.WriteString("'. This command was removed in the CLI redesign.\n\n")
	sb.WriteString("Did you mean one of:\n")
	for _, r := range replacements {
		sb.WriteString("  diffah ")
		sb.WriteString(r.verb)
		sb.WriteString(" ")
		sb.WriteString(r.args)
		sb.WriteString("    # ")
		sb.WriteString(r.note)
		sb.WriteString("\n")
	}
	sb.WriteString("\nRun 'diffah --help' for the full command list.")
	return &cliErr{cat: errs.CategoryUser, msg: sb.String()}
}
```

- [ ] **Step 4: Run to verify pass**

```bash
go test ./cmd/ -run TestRemovedCommand -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/removed.go cmd/removed_test.go
git commit -m "feat(cmd): trap removed 'export'/'import' verbs with migration hint

Both old commands are registered as hidden cobra stubs that reject any
invocation with a multi-line error listing the replacement verb(s) and
their argument signatures. Exit code 2 (user error)."
```

---

## Stage 2 — Global flag rename and short flags

Rename the global `--output text|json` to `--format / -o` (matches kubectl; eliminates collision with positional OUTPUT and subcommand `--image-format`). Add `-q`, `-v` short aliases to existing booleans.

### Task 2.1: Rename global --output → --format with -o

**Files:**
- Modify: `cmd/root.go:25`, `cmd/root.go:108-112`, `cmd/root.go:15-17`
- Modify: `cmd/export.go`, `cmd/import.go`, `cmd/inspect.go`, `cmd/doctor.go` (any reference to `outputFormat`)
- Test: Extend `cmd/root_test.go` (create if needed) with a short-flag test.

- [ ] **Step 1: Find all usages of the old flag name and the `outputFormat` var**

```bash
grep -rn "outputFormat\|--output" cmd/ --include="*.go" | grep -v "OutputFormat\|output-format\|--output-format"
```

Expected locations (verify before editing):
- `cmd/root.go`: var declaration, flag registration, reference in `Execute`.
- `cmd/export.go:79`: `if outputFormat == outputJSON`.
- `cmd/import.go:79`: same.
- `cmd/inspect.go:39,53`: same.
- `cmd/doctor.go:69`: same.

- [ ] **Step 2: Write a failing test**

Append to `cmd/root_test.go`:

```go
func TestGlobalFormat_ShortFlagO_SetsJSON(t *testing.T) {
	var stdout bytes.Buffer
	// version is a zero-arg command that prints plain text; under -o json
	// it should still print (it doesn't override format), but we only care
	// that the flag parses successfully without "unknown flag".
	code := Run(&stdout, nil, "-o", "json", "version")
	require.Equal(t, 0, code)
}

func TestGlobalFormat_LongFlagFormat_SetsJSON(t *testing.T) {
	var stdout bytes.Buffer
	code := Run(&stdout, nil, "--format", "json", "version")
	require.Equal(t, 0, code)
}

func TestGlobalOutput_RemovedEmitsUnknownFlag(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "--output", "json", "version")
	require.NotEqual(t, 0, code)
	require.Contains(t, stderr.String(), "unknown flag")
}

func TestQuietShortFlag(t *testing.T) {
	var stdout bytes.Buffer
	code := Run(&stdout, nil, "-q", "version")
	require.Equal(t, 0, code)
}

func TestVerboseShortFlag(t *testing.T) {
	var stdout bytes.Buffer
	code := Run(&stdout, nil, "-v", "version")
	require.Equal(t, 0, code)
}
```

Make sure `cmd/root_test.go` has `package cmd` and necessary imports (`bytes`, `testing`, `github.com/stretchr/testify/require`).

- [ ] **Step 3: Run to verify failure**

```bash
go test ./cmd/ -run "TestGlobalFormat|TestGlobalOutput_Removed|TestQuietShortFlag|TestVerboseShortFlag" -v
```

Expected: FAIL — either "unknown flag `--format`" or short flag parsing errors.

- [ ] **Step 4: Apply the rename and add short flags**

In `cmd/root.go`, replace the existing flag registration block:

```go
// before:
var (
	logLevel     string
	logFormat    string
	quiet        bool
	verbose      bool
	progressMode string
	outputFormat string
)
```

with:

```go
var (
	logLevel     string
	logFormat    string
	quiet        bool
	verbose      bool
	progressMode string
	outputFormat string // renamed from global --output; backing var kept so subcommands keep referencing `outputFormat`
)
```

Then in `init()`:

```go
// before:
pf.StringVar(&outputFormat, "output", "text",
    "output format: text|json (applies to inspect/dry-run/doctor and error rendering)")
// and:
pf.BoolVar(&quiet, "quiet", false, "...")
pf.BoolVar(&verbose, "verbose", false, "...")
```

Replace with:

```go
pf.StringVarP(&outputFormat, "format", "o", "text",
    "rendering format: text|json (applies to inspect/dry-run/doctor and error output)")
pf.BoolVarP(&quiet, "quiet", "q", false, "suppress info logs and progress bars (level=warn)")
pf.BoolVarP(&verbose, "verbose", "v", false, "enable debug logs (level=debug)")
```

No changes are needed in any file that references `outputFormat` — the backing Go variable keeps its name. Only the flag on the command line changes.

- [ ] **Step 5: Run the targeted tests**

```bash
go test ./cmd/ -run "TestGlobalFormat|TestGlobalOutput_Removed|TestQuietShortFlag|TestVerboseShortFlag" -v
```

Expected: PASS.

- [ ] **Step 6: Run the whole cmd package**

```bash
go test ./cmd/ -v
```

Expected: PASS. If any existing test invokes the removed `--output json` flag, update it to `--format json` or `-o json`.

- [ ] **Step 7: Commit**

```bash
git add cmd/root.go cmd/root_test.go
# plus any updated test files that used the old flag
git commit -m "feat(cmd): rename global --output to --format (-o) and add -q/-v

--output collided with positional OUTPUT and subcommand --image-format.
Renamed to --format with short -o (matches kubectl). Also adds short
aliases -q for --quiet and -v for --verbose."
```

---

## Stage 3 — `diff` subcommand (single-image)

Replaces the single-image path through the old `export`. `diff` takes two transport-prefixed image references and writes a delta archive.

### Task 3.1: Scaffold `diff` with args, flags, help, and the happy-path exporter call

**Files:**
- Create: `cmd/diff.go`
- Test: `cmd/diff_test.go`

- [ ] **Step 1: Write failing unit tests**

```go
// cmd/diff_test.go
package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiffCommand_HelpShowsArgumentsAndExamples(t *testing.T) {
	var stdout bytes.Buffer
	code := Run(&stdout, nil, "diff", "--help")
	require.Equal(t, 0, code)
	out := stdout.String()
	require.Contains(t, out, "Usage:")
	require.Contains(t, out, "diffah diff BASELINE-IMAGE TARGET-IMAGE DELTA-OUT")
	require.Contains(t, out, "Arguments:")
	require.Contains(t, out, "BASELINE-IMAGE")
	require.Contains(t, out, "TARGET-IMAGE")
	require.Contains(t, out, "DELTA-OUT")
	require.Contains(t, out, "Examples:")
	require.Contains(t, out, "docker-archive:/")
}

func TestDiffCommand_RejectsWrongArgCount(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "diff", "docker-archive:/tmp/only.tar")
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "'diff' requires 3 arguments")
	require.Contains(t, stderr.String(), "BASELINE-IMAGE, TARGET-IMAGE, DELTA-OUT")
	require.Contains(t, stderr.String(), "got 1")
}

func TestDiffCommand_RejectsMissingTransportPrefix(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "diff",
		"/tmp/old.tar", "/tmp/new.tar", "/tmp/delta.tar")
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "missing transport prefix for BASELINE-IMAGE")
	require.Contains(t, stderr.String(), "Did you mean:  docker-archive:/tmp/old.tar")
}

func TestDiffCommand_RejectsReservedTransport(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "diff",
		"docker://registry/img:v1",
		"docker-archive:/tmp/new.tar",
		"/tmp/delta.tar")
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "is reserved but not yet implemented")
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./cmd/ -run TestDiffCommand -v
```

Expected: FAIL — no `diff` subcommand registered.

- [ ] **Step 3: Implement the subcommand (happy path skeleton first)**

```go
// cmd/diff.go
package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/exporter"
)

var diffFlags = struct {
	platform   string
	compress   string
	intraLayer string
	dryRun     bool
}{}

const diffExample = `  # Compute a single-image delta
  diffah diff docker-archive:/tmp/old.tar docker-archive:/tmp/new.tar delta.tar

  # Cross-format (oci-archive baseline, docker-archive target)
  diffah diff oci-archive:/tmp/old.tar docker-archive:/tmp/new.tar delta.tar

  # Dry-run — plan without writing
  diffah diff --dry-run docker-archive:/tmp/old.tar docker-archive:/tmp/new.tar delta.tar`

func newDiffCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "diff BASELINE-IMAGE TARGET-IMAGE DELTA-OUT",
		Short: "Compute a single-image delta archive.",
		Args: requireArgs("diff",
			[]string{"BASELINE-IMAGE", "TARGET-IMAGE", "DELTA-OUT"},
			"diffah diff docker-archive:/tmp/old.tar docker-archive:/tmp/new.tar delta.tar"),
		Example: diffExample,
		Annotations: map[string]string{
			"arguments": "  BASELINE-IMAGE   older image to diff against (transport:path; see below)\n" +
				"  TARGET-IMAGE     newer image whose contents become the diff target\n" +
				"  DELTA-OUT        filesystem path to write the delta archive",
		},
		RunE: runDiff,
	}
	f := c.Flags()
	f.StringVar(&diffFlags.platform, "platform", "linux/amd64", "target platform")
	f.StringVar(&diffFlags.compress, "compress", "", "compression algorithm")
	f.StringVar(&diffFlags.intraLayer, "intra-layer", "auto", "intra-layer diff mode (auto|off|required)")
	f.BoolVarP(&diffFlags.dryRun, "dry-run", "n", false, "plan without writing the delta")
	installUsageTemplate(c)
	return c
}

func init() { rootCmd.AddCommand(newDiffCommand()) }

func runDiff(cmd *cobra.Command, args []string) error {
	baseline, err := ParseImageRef("BASELINE-IMAGE", args[0])
	if err != nil {
		return err
	}
	target, err := ParseImageRef("TARGET-IMAGE", args[1])
	if err != nil {
		return err
	}
	deltaOut := args[2]

	opts := exporter.Options{
		Pairs: []exporter.Pair{{
			Name:         "default",
			BaselinePath: baseline.Path,
			TargetPath:   target.Path,
		}},
		Platform:         diffFlags.platform,
		Compress:         diffFlags.compress,
		IntraLayer:       diffFlags.intraLayer,
		OutputPath:       deltaOut,
		ToolVersion:      version,
		ProgressReporter: newProgressReporter(cmd.ErrOrStderr()),
	}

	ctx := context.Background()
	if diffFlags.dryRun {
		stats, err := exporter.DryRun(ctx, opts)
		if err != nil {
			return err
		}
		if outputFormat == outputJSON {
			return writeJSON(cmd.OutOrStdout(), exportDryRunJSON(stats))
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"delta would ship %d blobs across %d images (%d bytes archive)\n",
			stats.TotalBlobs, stats.TotalImages, stats.ArchiveSize)
		return nil
	}
	if err := exporter.Export(ctx, opts); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", deltaOut)
	return nil
}
```

Note: `exportDryRunJSON` still lives in `cmd/export.go`; it will move to a shared spot in Stage 7 cleanup. For now, keep the reference — both files live in the same `cmd` package so the symbol is reachable.

- [ ] **Step 4: Run tests to verify pass**

```bash
go test ./cmd/ -run TestDiffCommand -v
```

Expected: PASS for help, arg-count, missing-transport, reserved-transport tests.

- [ ] **Step 5: Commit**

```bash
git add cmd/diff.go cmd/diff_test.go
git commit -m "feat(cmd): add 'diff' subcommand for single-image delta

Takes BASELINE-IMAGE TARGET-IMAGE DELTA-OUT positionals with strict
transport prefixes. Surfaces --platform/--compress/--intra-layer
flags and --dry-run (-n). Uses the shared Arguments-section help
template and requireArgs validator."
```

---

### Task 3.2: Integration test for `diff` round-trip happy path

**Files:**
- Create: `cmd/diff_integration_test.go`

- [ ] **Step 1: Write the failing integration test**

```go
//go:build integration

package cmd_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiffCommand_WithFixtures(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	out := filepath.Join(t.TempDir(), "delta.tar")

	cmd := exec.Command(bin,
		"diff",
		"docker-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		"docker-archive:"+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		out,
	)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	require.NoError(t, err, "stderr: %s", stderr.String())

	require.Contains(t, string(output), "wrote "+out)
	info, err := os.Stat(out)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}

func TestDiffCommand_DryRun(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	out := filepath.Join(t.TempDir(), "delta.tar")

	cmd := exec.Command(bin,
		"diff",
		"--dry-run",
		"docker-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		"docker-archive:"+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		out,
	)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))

	_, err = os.Stat(out)
	require.True(t, os.IsNotExist(err), "dry-run should not write the archive")
	require.True(t, strings.Contains(string(output), "would ship"),
		"stdout: %s", string(output))
}
```

Note the fixture paths come from `testdata/fixtures/v1_oci.tar` and `v2_oci.tar` (already used by the old `export_integration_test.go`). They contain OCI tars but `docker-archive:` prefix is still accepted by the library's sniff inside `OpenArchiveRef`. If a test fails because the library rejects the prefix, switch the prefix to `oci-archive:` in both test and example.

- [ ] **Step 2: Run to verify the build**

```bash
go test -tags integration ./cmd/ -run TestDiffCommand_WithFixtures -v
go test -tags integration ./cmd/ -run TestDiffCommand_DryRun -v
```

If the fixture content doesn't sniff as `docker-archive`, the exporter will fail. Check with:

```bash
tar -tf testdata/fixtures/v1_oci.tar | head -5
```

If you see `oci-layout`, change the test's transport prefix to `oci-archive:` and update the `diffExample` string in `cmd/diff.go` accordingly (to avoid giving users a broken example).

- [ ] **Step 3: Commit**

```bash
git add cmd/diff_integration_test.go
# if diff.go was updated with the correct fixture-matching transport:
git add cmd/diff.go
git commit -m "test(cmd): integration coverage for 'diff' subcommand

Covers happy-path round-trip with OCI archive fixtures and --dry-run
behavior (no file written, 'would ship' summary on stdout)."
```

---

## Stage 4 — `apply` subcommand (single-image)

Replaces the single-image path through the old `import`. `apply` consumes a delta archive + a baseline image, produces a reconstructed target.

### Task 4.1: Scaffold `apply` with positional args

**Files:**
- Create: `cmd/apply.go`
- Test: `cmd/apply_test.go`

- [ ] **Step 1: Write failing unit tests**

```go
// cmd/apply_test.go
package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyCommand_HelpShowsArgumentsAndExamples(t *testing.T) {
	var stdout bytes.Buffer
	code := Run(&stdout, nil, "apply", "--help")
	require.Equal(t, 0, code)
	out := stdout.String()
	require.Contains(t, out, "diffah apply DELTA-IN BASELINE-IMAGE TARGET-OUT")
	require.Contains(t, out, "Arguments:")
	require.Contains(t, out, "DELTA-IN")
	require.Contains(t, out, "BASELINE-IMAGE")
	require.Contains(t, out, "TARGET-OUT")
	require.Contains(t, out, "Examples:")
}

func TestApplyCommand_RejectsWrongArgCount(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "apply", "delta.tar", "docker-archive:/tmp/old.tar")
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "'apply' requires 3 arguments")
	require.Contains(t, stderr.String(), "DELTA-IN, BASELINE-IMAGE, TARGET-OUT")
}

func TestApplyCommand_RejectsBaselineMissingTransport(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "apply",
		"delta.tar", "/tmp/old.tar", "/tmp/out.tar")
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "missing transport prefix for BASELINE-IMAGE")
}
```

- [ ] **Step 2: Run to verify fail**

```bash
go test ./cmd/ -run TestApplyCommand -v
```

Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// cmd/apply.go
package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/importer"
)

var applyFlags = struct {
	imageFormat  string
	allowConvert bool
	dryRun       bool
}{}

const applyExample = `  # Reconstruct a single image from a delta + its baseline
  diffah apply delta.tar docker-archive:/tmp/old.tar docker-archive:/tmp/restored.tar

  # Write the reconstructed image as a directory (OCI layout)
  diffah apply --image-format dir delta.tar docker-archive:/tmp/old.tar /tmp/restored-dir

  # Dry-run — verify baseline reachability without writing
  diffah apply --dry-run delta.tar docker-archive:/tmp/old.tar /tmp/out.tar`

func newApplyCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "apply DELTA-IN BASELINE-IMAGE TARGET-OUT",
		Short: "Reconstruct a single image from a delta archive and a baseline.",
		Args: requireArgs("apply",
			[]string{"DELTA-IN", "BASELINE-IMAGE", "TARGET-OUT"},
			"diffah apply delta.tar docker-archive:/tmp/old.tar docker-archive:/tmp/restored.tar"),
		Example: applyExample,
		Annotations: map[string]string{
			"arguments": "  DELTA-IN         path to the delta archive produced by 'diffah diff'\n" +
				"  BASELINE-IMAGE   image to apply the delta on top of (transport:path)\n" +
				"  TARGET-OUT       filesystem path to write the reconstructed image",
		},
		RunE: runApply,
	}
	f := c.Flags()
	f.StringVar(&applyFlags.imageFormat, "image-format", "",
		"reconstructed image format: docker-archive|oci-archive|dir (default: match baseline)")
	f.BoolVar(&applyFlags.allowConvert, "allow-convert", false, "allow format conversion during apply")
	f.BoolVarP(&applyFlags.dryRun, "dry-run", "n", false, "verify baseline reachability without writing")
	installUsageTemplate(c)
	return c
}

func init() { rootCmd.AddCommand(newApplyCommand()) }

func runApply(cmd *cobra.Command, args []string) error {
	deltaIn := args[0]
	baseline, err := ParseImageRef("BASELINE-IMAGE", args[1])
	if err != nil {
		return err
	}
	targetOut := args[2]

	// Importer writes per-image under OutputPath which must be a directory.
	// For single-image apply, stage a scratch dir alongside TARGET-OUT and
	// rename the produced "default.<ext>" artifact to TARGET-OUT.
	scratch, err := os.MkdirTemp(filepath.Dir(targetOut), "diffah-apply-")
	if err != nil {
		return fmt.Errorf("create scratch dir: %w", err)
	}
	defer os.RemoveAll(scratch)

	opts := importer.Options{
		DeltaPath:        deltaIn,
		Baselines:        map[string]string{"default": baseline.Path},
		Strict:           true, // single-image apply always requires the baseline
		OutputPath:       scratch,
		OutputFormat:     applyFlags.imageFormat,
		AllowConvert:     applyFlags.allowConvert,
		ProgressReporter: newProgressReporter(cmd.ErrOrStderr()),
	}
	ctx := context.Background()

	if applyFlags.dryRun {
		report, err := importer.DryRun(ctx, opts)
		if err != nil {
			return err
		}
		if outputFormat == outputJSON {
			return writeJSON(cmd.OutOrStdout(), importDryRunJSON(report))
		}
		return renderDryRunReport(cmd.OutOrStdout(), report)
	}
	if err := importer.Import(ctx, opts); err != nil {
		return err
	}

	produced, err := findSingleImageArtifact(scratch)
	if err != nil {
		return err
	}
	if err := os.Rename(produced, targetOut); err != nil {
		return fmt.Errorf("move produced artifact to %s: %w", targetOut, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", targetOut)
	return nil
}

// findSingleImageArtifact returns the path of the single "default.*" entry
// written into the scratch directory by importer.Import for a single-image
// apply. It tolerates archive (default.tar) and dir (default/) forms.
func findSingleImageArtifact(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read scratch dir: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == "default" || name == "default.tar" ||
			filepath.Ext(name) == ".tar" {
			return filepath.Join(dir, name), nil
		}
	}
	return "", fmt.Errorf("no default image artifact produced in %s", dir)
}
```

- [ ] **Step 4: Run tests to verify pass**

```bash
go test ./cmd/ -run TestApplyCommand -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/apply.go cmd/apply_test.go
git commit -m "feat(cmd): add 'apply' subcommand for single-image reconstruction

Takes DELTA-IN BASELINE-IMAGE TARGET-OUT positionals. Renames the
old --output-format flag to --image-format (scoped here).
Single-image apply uses a scratch dir under the target's parent
then renames the 'default' artifact into TARGET-OUT for byte-level
determinism."
```

---

### Task 4.2: Integration test for `apply` round-trip

**Files:**
- Create: `cmd/apply_integration_test.go`

- [ ] **Step 1: Write the failing integration test**

```go
//go:build integration

package cmd_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyCommand_RoundTrip(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	tmp := t.TempDir()
	delta := filepath.Join(tmp, "delta.tar")
	restored := filepath.Join(tmp, "restored.tar")

	// diff
	{
		cmd := exec.Command(bin,
			"diff",
			"docker-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
			"docker-archive:"+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
			delta,
		)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}

	// apply
	{
		cmd := exec.Command(bin,
			"apply",
			delta,
			"docker-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
			restored,
		)
		cmd.Dir = root
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		require.NoError(t, err, "stderr: %s", stderr.String())
		require.Contains(t, string(out), "wrote "+restored)
	}

	info, err := os.Stat(restored)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}
```

- [ ] **Step 2: Run and verify pass**

```bash
go test -tags integration ./cmd/ -run TestApplyCommand_RoundTrip -v
```

If the fixture transport mismatches, switch the prefix to `oci-archive:` and update `applyExample` in `cmd/apply.go` similarly.

- [ ] **Step 3: Commit**

```bash
git add cmd/apply_integration_test.go
git commit -m "test(cmd): integration coverage for 'apply' round-trip"
```

---

## Stage 5 — `bundle` subcommand (multi-image)

### Task 5.1: Scaffold `bundle` with BUNDLE-SPEC DELTA-OUT positionals

**Files:**
- Create: `cmd/bundle.go`
- Test: `cmd/bundle_test.go`

- [ ] **Step 1: Write failing tests**

```go
// cmd/bundle_test.go
package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBundleCommand_HelpShowsArgumentsAndExamples(t *testing.T) {
	var stdout bytes.Buffer
	code := Run(&stdout, nil, "bundle", "--help")
	require.Equal(t, 0, code)
	out := stdout.String()
	require.Contains(t, out, "diffah bundle BUNDLE-SPEC DELTA-OUT")
	require.Contains(t, out, "Arguments:")
	require.Contains(t, out, "BUNDLE-SPEC")
	require.Contains(t, out, "DELTA-OUT")
}

func TestBundleCommand_RejectsWrongArgCount(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "bundle", "only-one.json")
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "'bundle' requires 2 arguments")
}

func TestBundleCommand_RejectsMissingSpecFile(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "bundle", "/tmp/does-not-exist.json", "bundle.tar")
	require.NotEqual(t, 0, code)
	require.Contains(t, stderr.String(), "bundle spec")
}

func TestBundleCommand_AcceptsSpecFile(t *testing.T) {
	tmp := t.TempDir()
	specPath := filepath.Join(tmp, "spec.json")
	spec := map[string]any{
		"pairs": []map[string]string{
			{"name": "app", "baseline": "b.tar", "target": "t.tar"},
		},
	}
	raw, _ := json.Marshal(spec)
	require.NoError(t, os.WriteFile(specPath, raw, 0o600))

	var stderr bytes.Buffer
	code := Run(nil, &stderr, "bundle", "--dry-run", specPath, filepath.Join(tmp, "bundle.tar"))
	// We expect dry-run to fail because b.tar/t.tar don't exist, but
	// the error must come from the exporter after spec parsing, not
	// from CLI arg validation.
	require.NotEqual(t, 2, code, "exit 2 indicates CLI rejected args; stderr: %s", stderr.String())
}
```

- [ ] **Step 2: Run to verify fail**

```bash
go test ./cmd/ -run TestBundleCommand -v
```

Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// cmd/bundle.go
package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/exporter"
)

var bundleFlags = struct {
	platform   string
	compress   string
	intraLayer string
	dryRun     bool
}{}

const bundleExample = `  # Bundle multiple images using a spec file
  diffah bundle bundle.json bundle.tar

  # Dry-run (plan only)
  diffah bundle --dry-run bundle.json bundle.tar`

func newBundleCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "bundle BUNDLE-SPEC DELTA-OUT",
		Short: "Export a multi-image delta bundle driven by a spec file.",
		Args: requireArgs("bundle",
			[]string{"BUNDLE-SPEC", "DELTA-OUT"},
			"diffah bundle bundle.json bundle.tar"),
		Example: bundleExample,
		Annotations: map[string]string{
			"arguments": "  BUNDLE-SPEC   JSON spec listing per-image {name, baseline, target} triples\n" +
				"  DELTA-OUT     filesystem path to write the multi-image delta archive",
		},
		RunE: runBundle,
	}
	f := c.Flags()
	f.StringVar(&bundleFlags.platform, "platform", "linux/amd64", "target platform")
	f.StringVar(&bundleFlags.compress, "compress", "", "compression algorithm")
	f.StringVar(&bundleFlags.intraLayer, "intra-layer", "auto", "intra-layer diff mode (auto|off|required)")
	f.BoolVarP(&bundleFlags.dryRun, "dry-run", "n", false, "plan without writing the bundle")
	installUsageTemplate(c)
	return c
}

func init() { rootCmd.AddCommand(newBundleCommand()) }

func runBundle(cmd *cobra.Command, args []string) error {
	specPath := args[0]
	deltaOut := args[1]

	spec, err := diff.ParseBundleSpec(specPath)
	if err != nil {
		return fmt.Errorf("parse bundle spec: %w", err)
	}
	pairs := make([]exporter.Pair, len(spec.Pairs))
	for i, p := range spec.Pairs {
		pairs[i] = exporter.Pair{
			Name:         p.Name,
			BaselinePath: p.Baseline,
			TargetPath:   p.Target,
		}
	}

	opts := exporter.Options{
		Pairs:            pairs,
		Platform:         bundleFlags.platform,
		Compress:         bundleFlags.compress,
		IntraLayer:       bundleFlags.intraLayer,
		OutputPath:       deltaOut,
		ToolVersion:      version,
		ProgressReporter: newProgressReporter(cmd.ErrOrStderr()),
	}
	ctx := context.Background()

	if bundleFlags.dryRun {
		stats, err := exporter.DryRun(ctx, opts)
		if err != nil {
			return err
		}
		if outputFormat == outputJSON {
			return writeJSON(cmd.OutOrStdout(), exportDryRunJSON(stats))
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"bundle would ship %d blobs across %d images (%d bytes archive)\n",
			stats.TotalBlobs, stats.TotalImages, stats.ArchiveSize)
		return nil
	}
	if err := exporter.Export(ctx, opts); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", deltaOut)
	return nil
}
```

- [ ] **Step 4: Run tests to verify pass**

```bash
go test ./cmd/ -run TestBundleCommand -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/bundle.go cmd/bundle_test.go
git commit -m "feat(cmd): add 'bundle' subcommand for multi-image bundling

Takes BUNDLE-SPEC DELTA-OUT positionals; replaces the old
--pair/--bundle flag surface. Spec file is parsed by the existing
diff.ParseBundleSpec helper."
```

---

### Task 5.2: Integration test for `bundle`

**Files:**
- Create: `cmd/bundle_integration_test.go`

- [ ] **Step 1: Write the test**

```go
//go:build integration

package cmd_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBundleCommand_WithSpec(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	tmp := t.TempDir()

	spec := map[string]any{
		"pairs": []map[string]string{{
			"name":     "app",
			"baseline": filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
			"target":   filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		}},
	}
	raw, _ := json.MarshalIndent(spec, "", "  ")
	specPath := filepath.Join(tmp, "bundle.json")
	require.NoError(t, os.WriteFile(specPath, raw, 0o600))

	out := filepath.Join(tmp, "bundle.tar")
	cmd := exec.Command(bin, "bundle", specPath, out)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))

	info, err := os.Stat(out)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}
```

- [ ] **Step 2: Run and verify**

```bash
go test -tags integration ./cmd/ -run TestBundleCommand_WithSpec -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add cmd/bundle_integration_test.go
git commit -m "test(cmd): integration coverage for 'bundle' with spec file"
```

---

## Stage 6 — `unbundle` subcommand (multi-image)

### Task 6.1: Scaffold `unbundle`

**Files:**
- Create: `cmd/unbundle.go`
- Test: `cmd/unbundle_test.go`

- [ ] **Step 1: Write failing tests**

```go
// cmd/unbundle_test.go
package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnbundleCommand_HelpShowsArguments(t *testing.T) {
	var stdout bytes.Buffer
	code := Run(&stdout, nil, "unbundle", "--help")
	require.Equal(t, 0, code)
	out := stdout.String()
	require.Contains(t, out, "diffah unbundle DELTA-IN BASELINE-SPEC OUTPUT-DIR")
	require.Contains(t, out, "Arguments:")
	require.Contains(t, out, "DELTA-IN")
	require.Contains(t, out, "BASELINE-SPEC")
	require.Contains(t, out, "OUTPUT-DIR")
}

func TestUnbundleCommand_RejectsWrongArgCount(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(nil, &stderr, "unbundle", "d.tar", "b.json")
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "'unbundle' requires 3 arguments")
}

func TestUnbundleCommand_AcceptsStrict(t *testing.T) {
	var stdout bytes.Buffer
	code := Run(&stdout, nil, "unbundle", "--help")
	require.Equal(t, 0, code)
	require.Contains(t, stdout.String(), "--strict")
}
```

- [ ] **Step 2: Run to verify fail**

```bash
go test ./cmd/ -run TestUnbundleCommand -v
```

Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// cmd/unbundle.go
package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/importer"
)

var unbundleFlags = struct {
	imageFormat  string
	allowConvert bool
	strict       bool
	dryRun       bool
}{}

const unbundleExample = `  # Reconstruct all images from a bundle using a baseline spec
  diffah unbundle bundle.tar baselines.json ./restored/

  # Strict mode — fail if any baseline referenced by the bundle is missing
  diffah unbundle --strict bundle.tar baselines.json ./restored/

  # Write reconstructed images as directories instead of tars
  diffah unbundle --image-format dir bundle.tar baselines.json ./restored/`

func newUnbundleCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "unbundle DELTA-IN BASELINE-SPEC OUTPUT-DIR",
		Short: "Reconstruct all images from a multi-image delta bundle.",
		Args: requireArgs("unbundle",
			[]string{"DELTA-IN", "BASELINE-SPEC", "OUTPUT-DIR"},
			"diffah unbundle bundle.tar baselines.json ./restored/"),
		Example: unbundleExample,
		Annotations: map[string]string{
			"arguments": "  DELTA-IN        path to the bundle archive produced by 'diffah bundle'\n" +
				"  BASELINE-SPEC   JSON spec mapping image name -> baseline path\n" +
				"  OUTPUT-DIR      directory where reconstructed images are written",
		},
		RunE: runUnbundle,
	}
	f := c.Flags()
	f.StringVar(&unbundleFlags.imageFormat, "image-format", "",
		"reconstructed image format: docker-archive|oci-archive|dir (default: match baseline)")
	f.BoolVar(&unbundleFlags.allowConvert, "allow-convert", false, "allow format conversion")
	f.BoolVar(&unbundleFlags.strict, "strict", false, "require every baseline referenced by the bundle")
	f.BoolVarP(&unbundleFlags.dryRun, "dry-run", "n", false, "verify reachability without writing")
	installUsageTemplate(c)
	return c
}

func init() { rootCmd.AddCommand(newUnbundleCommand()) }

func runUnbundle(cmd *cobra.Command, args []string) error {
	deltaIn := args[0]
	specPath := args[1]
	outDir := args[2]

	spec, err := diff.ParseBaselineSpec(specPath)
	if err != nil {
		return fmt.Errorf("parse baseline spec: %w", err)
	}
	baselines := make(map[string]string, len(spec.Baselines))
	for name, path := range spec.Baselines {
		baselines[name] = path
	}

	opts := importer.Options{
		DeltaPath:        deltaIn,
		Baselines:        baselines,
		Strict:           unbundleFlags.strict,
		OutputPath:       outDir,
		OutputFormat:     unbundleFlags.imageFormat,
		AllowConvert:     unbundleFlags.allowConvert,
		ProgressReporter: newProgressReporter(cmd.ErrOrStderr()),
	}
	ctx := context.Background()

	if unbundleFlags.dryRun {
		report, err := importer.DryRun(ctx, opts)
		if err != nil {
			return err
		}
		if outputFormat == outputJSON {
			return writeJSON(cmd.OutOrStdout(), importDryRunJSON(report))
		}
		return renderDryRunReport(cmd.OutOrStdout(), report)
	}
	if err := importer.Import(ctx, opts); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote images to %s\n", outDir)
	return nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./cmd/ -run TestUnbundleCommand -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/unbundle.go cmd/unbundle_test.go
git commit -m "feat(cmd): add 'unbundle' subcommand for multi-image reconstruction

Takes DELTA-IN BASELINE-SPEC OUTPUT-DIR positionals plus --strict,
--image-format, --allow-convert, --dry-run. Baseline spec is parsed
by the existing diff.ParseBaselineSpec helper."
```

---

### Task 6.2: Integration test for `bundle` → `unbundle` round-trip

**Files:**
- Create: `cmd/unbundle_integration_test.go`

- [ ] **Step 1: Write the test**

```go
//go:build integration

package cmd_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnbundleCommand_BundleRoundTrip(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	tmp := t.TempDir()

	bundleSpec := map[string]any{
		"pairs": []map[string]string{{
			"name":     "app",
			"baseline": filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
			"target":   filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		}},
	}
	specPath := filepath.Join(tmp, "bundle.json")
	raw, _ := json.Marshal(bundleSpec)
	require.NoError(t, os.WriteFile(specPath, raw, 0o600))

	bundleOut := filepath.Join(tmp, "bundle.tar")
	cmd := exec.Command(bin, "bundle", specPath, bundleOut)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	baselineSpec := map[string]any{
		"baselines": map[string]string{
			"app": filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		},
	}
	baselinePath := filepath.Join(tmp, "baselines.json")
	raw, _ = json.Marshal(baselineSpec)
	require.NoError(t, os.WriteFile(baselinePath, raw, 0o600))

	restored := filepath.Join(tmp, "restored")
	require.NoError(t, os.MkdirAll(restored, 0o755))
	cmd = exec.Command(bin, "unbundle", bundleOut, baselinePath, restored)
	cmd.Dir = root
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	entries, err := os.ReadDir(restored)
	require.NoError(t, err)
	require.NotEmpty(t, entries, "expected at least one reconstructed image")
}
```

- [ ] **Step 2: Run**

```bash
go test -tags integration ./cmd/ -run TestUnbundleCommand_BundleRoundTrip -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add cmd/unbundle_integration_test.go
git commit -m "test(cmd): integration coverage for bundle -> unbundle round-trip"
```

---

## Stage 7 — Cleanup: delete old commands, update CHANGELOG, final test run

### Task 7.1: Delete `export.go` / `import.go` and move shared helpers

**Files:**
- Delete: `cmd/export.go`, `cmd/import.go`
- Modify: possibly extract `exportDryRunJSON` / `importDryRunJSON` / `renderDryRunReport` to shared files if they are still referenced.

- [ ] **Step 1: Identify symbols in `export.go` / `import.go` still referenced by new subcommands**

```bash
grep -n "exportDryRunJSON\|importDryRunJSON\|renderDryRunReport\|parsePairFlag\|parseBaselineFlag\|resolveExportPairs\|resolveImportBaselines" cmd/*.go
```

Expected: `exportDryRunJSON`, `importDryRunJSON`, `renderDryRunReport` are still used by `diff.go`, `apply.go`, `bundle.go`, `unbundle.go`. The pair/baseline parsers (`parsePairFlag`, `parseBaselineFlag`, `resolveExportPairs`, `resolveImportBaselines`) are no longer used.

- [ ] **Step 2: Create `cmd/dryrun.go` holding the still-shared helpers**

```go
// cmd/dryrun.go
package cmd

import (
	"fmt"
	"io"
	"time"

	"github.com/leosocy/diffah/pkg/exporter"
	"github.com/leosocy/diffah/pkg/importer"
)

func exportDryRunJSON(stats exporter.DryRunStats) any {
	images := make([]map[string]any, 0, len(stats.PerImage))
	for _, img := range stats.PerImage {
		images = append(images, map[string]any{
			"name":          img.Name,
			"shipped_blobs": img.ShippedBlobs,
			"archive_bytes": img.ArchiveSize,
		})
	}
	return map[string]any{
		"total_blobs":   stats.TotalBlobs,
		"total_images":  stats.TotalImages,
		"archive_bytes": stats.ArchiveSize,
		"images":        images,
	}
}

func importDryRunJSON(r importer.DryRunReport) any {
	images := make([]map[string]any, 0, len(r.Images))
	for _, img := range r.Images {
		entry := map[string]any{
			"name":                     img.Name,
			"baseline_manifest_digest": img.BaselineManifestDigest.String(),
			"target_manifest_digest":   img.TargetManifestDigest.String(),
			"baseline_provided":        img.BaselineProvided,
			"would_import":             img.WouldImport,
			"layer_count":              img.LayerCount,
			"archive_layer_count":      img.ArchiveLayerCount,
			"baseline_layer_count":     img.BaselineLayerCount,
			"patch_layer_count":        img.PatchLayerCount,
		}
		if img.SkipReason != "" {
			entry["skip_reason"] = img.SkipReason
		}
		images = append(images, entry)
	}
	return map[string]any{
		"feature":        r.Feature,
		"version":        r.Version,
		"tool":           r.Tool,
		"tool_version":   r.ToolVersion,
		"created_at":     r.CreatedAt.UTC().Format(time.RFC3339),
		"platform":       r.Platform,
		"images":         images,
		"blobs": map[string]any{
			"full_count":  r.Blobs.FullCount,
			"patch_count": r.Blobs.PatchCount,
			"full_bytes":  r.Blobs.FullBytes,
			"patch_bytes": r.Blobs.PatchBytes,
		},
		"archive_bytes":  r.ArchiveBytes,
		"requires_zstd":  r.RequiresZstd,
		"zstd_available": r.ZstdAvailable,
	}
}

func renderDryRunReport(w io.Writer, r importer.DryRunReport) error {
	fmt.Fprintf(w, "archive: feature=%s version=%s platform=%s\n",
		r.Feature, r.Version, r.Platform)
	fmt.Fprintf(w, "tool: %s %s, created %s\n",
		r.Tool, r.ToolVersion, r.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(w, "archive bytes: %d\n", r.ArchiveBytes)
	fmt.Fprintf(w, "blobs: %d (full: %d, patch: %d) — full: %d B, patch: %d B\n",
		r.Blobs.FullCount+r.Blobs.PatchCount,
		r.Blobs.FullCount, r.Blobs.PatchCount,
		r.Blobs.FullBytes, r.Blobs.PatchBytes)
	fmt.Fprintf(w, "images: %d\n", len(r.Images))
	for _, img := range r.Images {
		state := "would import"
		if !img.WouldImport {
			state = fmt.Sprintf("skip — %s", img.SkipReason)
		}
		fmt.Fprintf(w, "  %-20s target=%s (%s)\n", img.Name, img.TargetManifestDigest, state)
		fmt.Fprintf(w, "    layers: %d total — %d shipped, %d from baseline, %d patched\n",
			img.LayerCount, img.ArchiveLayerCount, img.BaselineLayerCount, img.PatchLayerCount)
	}
	return nil
}
```

- [ ] **Step 3: Delete the old files**

```bash
git rm cmd/export.go cmd/import.go cmd/export_integration_test.go cmd/import_integration_test.go cmd/export_json_test.go cmd/import_json_test.go
```

Inspect any other test files that reference old commands (e.g. `exit_integration_test.go`) and update the command names:

```bash
grep -rn "\"export\"\|\"import\"" cmd/*_test.go | grep -v removed_test.go
```

For each match, replace the top-level verb with the appropriate new one, OR keep the test as-is if it's the removed-command trap test.

- [ ] **Step 4: Build and run full cmd tests**

```bash
go build ./...
go test ./cmd/ -v
```

Expected: PASS. Compile errors likely if we missed a symbol — resolve by finishing the migration of any lingering references.

- [ ] **Step 5: Run integration tests**

```bash
go test -tags integration ./cmd/ -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/dryrun.go
git add -u cmd/
git commit -m "refactor(cmd): delete export/import; consolidate dry-run helpers

Old single/multi-image flag-based commands are gone. Shared dry-run
renderers move to cmd/dryrun.go. Integration tests for the removed
verbs are dropped; the new diff/apply/bundle/unbundle integration
tests and the removed-command trap cover the surface."
```

---

### Task 7.2: Update CHANGELOG and run full acceptance

**Files:**
- Modify: `CHANGELOG.md`
- Run: full test suite, lint, manual smoke test.

- [ ] **Step 1: Update `CHANGELOG.md`**

Append under the existing `## [Unreleased]` section (keep the existing bundle entries; add the CLI redesign as its own breaking-change block):

```markdown
### CLI redesign (skopeo-inspired)

- **Removed:** `diffah export` and `diffah import`. Old invocations now
  error with a migration hint pointing at the new verbs.
- **Removed:** `--pair NAME=BASELINE,TARGET` and `--baseline NAME=PATH`
  composite flags.
- **Added:** `diffah diff BASELINE-IMAGE TARGET-IMAGE DELTA-OUT` — single
  image delta.
- **Added:** `diffah apply DELTA-IN BASELINE-IMAGE TARGET-OUT` — single
  image reconstruction.
- **Added:** `diffah bundle BUNDLE-SPEC DELTA-OUT` — multi-image bundle
  driven by a JSON spec file (positional, not a flag).
- **Added:** `diffah unbundle DELTA-IN BASELINE-SPEC OUTPUT-DIR` —
  multi-image reconstruction driven by a JSON baseline spec.
- **Changed:** image references on `*-IMAGE` positionals now require a
  transport prefix (`docker-archive:` or `oci-archive:`). Bare paths
  error with a "Did you mean" hint.
- **Renamed:** global `--output text|json` → `--format text|json` (short
  `-o`) to eliminate collision with positional OUTPUT / subcommand
  `--image-format`.
- **Renamed:** subcommand `--output-format docker-archive|oci-archive|dir`
  → `--image-format` (scoped to `apply` / `unbundle`).
- **Added:** short flags `-q` (`--quiet`), `-v` (`--verbose`), `-n`
  (`--dry-run`).
- **Added:** Arguments section in `--help` output with per-arg purpose
  and accepted-transport list; error messages include usage line,
  copy-paste-ready example, and a `Run '<cmd> --help'` pointer.
```

- [ ] **Step 2: Run the full pipeline**

```bash
# Formatter, vet, build
go fmt ./...
go vet ./...
go build ./...

# Unit tests
go test ./... -v

# Integration tests
go test -tags integration ./cmd/ -v
```

Expected: all PASS. Resolve any failure before committing.

- [ ] **Step 3: Manual smoke test (acceptance)**

Build the binary, then run each of the scenarios from spec §8.5:

```bash
go build -o /tmp/diffah-new .

# arg-count guidance
/tmp/diffah-new diff 2>&1 | head -20
# expect: "'diff' requires 3 arguments" + usage + example + --help pointer

# missing-transport guidance
/tmp/diffah-new diff /tmp/old.tar /tmp/new.tar /tmp/delta.tar 2>&1 | head -20
# expect: "missing transport prefix" + supported list + "Did you mean"

# removed-command redirection
/tmp/diffah-new export --pair app=v1.tar,v2.tar bundle.tar 2>&1 | head -20
# expect: "unknown command 'export'" + replacement verbs

# end-to-end round-trip (use real fixtures from testdata/fixtures/)
/tmp/diffah-new diff \
    docker-archive:$(pwd)/testdata/fixtures/v1_oci.tar \
    docker-archive:$(pwd)/testdata/fixtures/v2_oci.tar \
    /tmp/delta.tar
/tmp/diffah-new inspect /tmp/delta.tar
/tmp/diffah-new apply /tmp/delta.tar \
    docker-archive:$(pwd)/testdata/fixtures/v1_oci.tar \
    /tmp/restored.tar
# expect: round-trip succeeds, stats match
```

If any scenario produces something other than the expected output described in the spec, **stop and fix**; do not commit until acceptance passes.

- [ ] **Step 4: Final commit**

```bash
git add CHANGELOG.md
git commit -m "docs: CHANGELOG entry for CLI redesign

Documents the breaking changes introduced by the skopeo-inspired
CLI refactor: new verbs, transport prefixes, flag renames, new
short flags, and help/error UX guarantees."
```

- [ ] **Step 5: Push and open the PR (user-driven; only if ready)**

```bash
git push -u origin spec/cli-redesign-skopeo-inspired
gh pr create --title "CLI redesign: skopeo-inspired diff/apply/bundle/unbundle" \
  --body "See docs/superpowers/specs/2026-04-23-cli-redesign-skopeo-inspired-design.md for the full design. Resolves brainstorming choices A1/B1/C1/D1."
```

(Stop here if user wants to review locally before pushing.)

---

## Self-Review

Spec coverage verified against each § of the design doc:

| Spec § | Plan coverage |
|---|---|
| §1 Motivation | Tasks 7.2 smoke tests replay the field scenarios. |
| §2 Goals | All four verbs (§3.1–§3.3) covered by Tasks 3.x–6.x; transport grammar by Task 1.1; help UX by Task 1.3; error UX by Tasks 1.1, 1.2, 1.4; flag cleanup by Task 2.1. |
| §2 Non-goals | Plan does not touch `pkg/diff`, `pkg/exporter`, `pkg/importer`, or `pkg/progress`. Verified by Task 7.1 grep step. |
| §3 Command surface | §3.1 diff+apply: Tasks 3.1–4.2. §3.2 bundle+unbundle: Tasks 5.1–6.2. §3.3 unchanged verbs: no-op (inspect already positional; doctor/version untouched). §3.4 removed: Task 1.4. |
| §4 Transport grammar | Task 1.1. |
| §5 Flag surface | §5.1 global: Task 2.1. §5.2/3/4/5 subcommand: Tasks 3.1/4.1/5.1/6.1. §5.6 rename table: Tasks 2.1 + 7.1. |
| §6 Error/help UX | §6.1 template: Task 1.3. §6.2 arg count: Task 1.2. §6.3 removed-command: Task 1.4. §6.4 missing transport: Task 1.1. §6.5 unknown flag: cobra built-in, no task needed (verified in Task 7.2 smoke). §6.7 JSON errors: reuse existing `writeJSONError`; Task 1.1 error types set Category correctly. |
| §7 Architecture | Package layout matches §7.1. §7.2 parser: Task 1.1. §7.3 spec parsers reused in Tasks 5.1 / 6.1. §7.4 help template: Task 1.3. §7.5 Args validator: Task 1.2. §7.6 removed-command trap: Task 1.4. |
| §8 Testing | Unit: Tasks 1.1–1.4, 2.1, 3.1, 4.1, 5.1, 6.1. Integration: Tasks 3.2, 4.2, 5.2, 6.2. JSON: error rendering continues working via existing tests; JSON dry-run is exercised indirectly. Golden help: Task 3.1 (and each subcommand task) asserts help contains Arguments/Examples sections. Manual acceptance: Task 7.2 Step 3. |
| §9 Migration notes | Task 7.2. |
| §10 Rollout | Matches stage ordering. |

**Placeholder scan:** no TBDs; all code blocks are complete; all grep-and-edit steps list the file and line ranges to check.

**Type consistency verified:**
- `ImageRef{Transport, Path}` shape is consistent across all consumers (Tasks 1.1, 3.1, 4.1).
- `exporter.Options` fields (Pairs, Platform, Compress, IntraLayer, OutputPath, ToolVersion, ProgressReporter) used identically in Tasks 3.1 and 5.1.
- `importer.Options` fields (DeltaPath, Baselines, Strict, OutputPath, OutputFormat, AllowConvert, ProgressReporter) used identically in Tasks 4.1 and 6.1.
- `requireArgs(verb, argNames, example)` signature consistent across all callers.
- `installUsageTemplate(cmd)` signature consistent across all callers.
- Shared JSON helpers `exportDryRunJSON`, `importDryRunJSON`, `renderDryRunReport` referenced consistently; moved to `cmd/dryrun.go` in Task 7.1.
- Global flag variable name `outputFormat` is kept in Task 2.1 to minimize blast radius; only the CLI-surface flag name changes.

**Known deviations / fixture risk:**
- Tasks 3.2 / 4.2 / 5.2 / 6.2 use `testdata/fixtures/v1_oci.tar` + `v2_oci.tar`. The transport prefix used in examples/tests (`docker-archive:`) depends on what the fixture actually is. If `tar -tf` on the fixture shows `oci-layout`, switch the example and test to `oci-archive:`. Noted explicitly in Task 3.2 Step 2.
