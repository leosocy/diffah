# Phase 5.2 — YAML Config File Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `~/.diffah/config.yaml` (or `$DIFFAH_CONFIG`) YAML configuration file supplying defaults for nine widely-repeated CLI flags, plus `diffah config show / init / validate` helper subcommands. CLI flags always override config; absent file = built-in defaults; malformed file = hard fail.

**Architecture:** New package `pkg/config/` defines the `Config` struct, defaults, viper-backed `Load`, `Validate`, and `ApplyTo(flags, *Config)`. `ApplyTo` walks the cobra `pflag.FlagSet` and overwrites `DefValue` for known fields **only when the flag has not been explicitly set on the command line** (`flag.Changed == false`). The root command's existing `PersistentPreRunE` hook is extended to call `Load` once per process, then `ApplyTo` against the running command's flags. Three new `cmd/config_*.go` files implement the helper subcommands.

**Tech Stack:** Go 1.25, `github.com/spf13/cobra` (already a direct dep), `github.com/spf13/viper` v1 (new direct dep, brings yaml.v3 transitively for parsing), `github.com/stretchr/testify/require`, existing `pkg/diff/errs` taxonomy.

**Spec reference:** `docs/superpowers/specs/2026-04-29-phase5-dx-polish-design.md` §5.

**Out of scope** (per spec §3 / §11):
- Per-field environment variables (only `$DIFFAH_CONFIG` path override).
- Project-local layered config (no `./diffah.yaml` merge).
- Validation rules beyond schema/type (e.g., "platform must be one of ...") — defer to future PR.

---

## File plan

| File | Action | Responsibility |
|---|---|---|
| `go.mod` | modify | Add `github.com/spf13/viper` direct dep |
| `pkg/config/config.go` | create | `Config` struct + per-field viper key tags |
| `pkg/config/defaults.go` | create | Built-in default constants (single source of truth) |
| `pkg/config/load.go` | create | `Load(path string) (*Config, error)` — viper-backed |
| `pkg/config/validate.go` | create | `Validate(path string) error` — wraps Load |
| `pkg/config/apply.go` | create | `ApplyTo(flags *pflag.FlagSet, *Config)` — sets unchanged flags |
| `pkg/config/errors.go` | create | `ConfigError` type implementing `errs.Categorized → CategoryUser` |
| `pkg/config/config_test.go` | create | Defaults + struct contract tests |
| `pkg/config/load_test.go` | create | Load: missing/valid/malformed/unknown-field/type-mismatch |
| `pkg/config/validate_test.go` | create | Validate behaves like Load with discarded result |
| `pkg/config/apply_test.go` | create | ApplyTo: defaults set when !Changed; preserved when Changed |
| `cmd/config.go` | create | `diffah config` parent + subcommand registration |
| `cmd/config_show.go` | create | `diffah config show` |
| `cmd/config_init.go` | create | `diffah config init [PATH] [--force]` |
| `cmd/config_validate.go` | create | `diffah config validate [PATH]` |
| `cmd/config_test.go` | create | Subcommand text + json output tests |
| `cmd/config_integration_test.go` | create | `DIFFAH_CONFIG` drives `diffah diff --dry-run` defaults |
| `cmd/root.go` | modify | Extend `PersistentPreRunE` to Load config + ApplyTo |
| `cmd/diff.go` | modify | (no flag change; ApplyTo drives defaults via root hook) |
| `cmd/bundle.go` | modify | (same) |
| `cmd/apply.go` | modify | (same) |
| `cmd/unbundle.go` | modify | (same) |
| `CHANGELOG.md` | modify | Phase 5.2 entry under `[Unreleased]` |

The four command files (`diff`/`bundle`/`apply`/`unbundle`) need no changes themselves — `ApplyTo` operates on `cmd.Flags()` from the root hook. Listed only to make the integration surface explicit.

---

## Phase 1 — Package foundation

### Task 1: Add viper dependency

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Run `go get github.com/spf13/viper`**

```bash
cd /Users/leosocy/workspace/repos/myself/diffah
go get github.com/spf13/viper
```

Expected: viper added under `require ()`. Several transitive deps appear (yaml.v3, fsnotify, etc.) — these come with viper.

- [ ] **Step 2: Verify build still passes**

```bash
go build ./...
```

Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore(deps): add spf13/viper for Phase 5 config

Required by pkg/config (Phase 5.2). YAML parsing via yaml.v3
transitively. AutomaticEnv intentionally not enabled — only
\$DIFFAH_CONFIG path override is supported per spec §3.

Refs: docs/superpowers/plans/2026-04-29-phase5-config.md Task 1"
```

### Task 2: Define Config struct + defaults

**Files:**
- Create: `pkg/config/config.go`
- Create: `pkg/config/defaults.go`
- Create: `pkg/config/config_test.go`

- [ ] **Step 1: Write the failing test** at `pkg/config/config_test.go`:

```go
package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDefault_ContainsAllNineFields(t *testing.T) {
	d := Default()

	require.Equal(t, "linux/amd64", d.Platform)
	require.Equal(t, "auto", d.IntraLayer)
	require.Equal(t, "", d.Authfile) // empty = use lookup chain
	require.Equal(t, 0, d.RetryTimes)
	require.Equal(t, time.Duration(0), d.RetryDelay)
	require.Equal(t, 22, d.ZstdLevel)
	require.Equal(t, "auto", d.ZstdWindowLog)
	require.Equal(t, 8, d.Workers)
	require.Equal(t, 3, d.Candidates)
}
```

- [ ] **Step 2: Run test, verify failure**

```bash
go test ./pkg/config/ -run TestDefault_ContainsAllNineFields -v
```

Expected: `package github.com/leosocy/diffah/pkg/config: no Go files`.

- [ ] **Step 3: Create `pkg/config/config.go`**

```go
// Package config loads diffah's optional YAML configuration file.
//
// Lookup order (first match wins for the file path):
//   1. $DIFFAH_CONFIG (must be absolute path)
//   2. ~/.diffah/config.yaml
//   3. (no file → built-in defaults)
//
// Per-field precedence (most → least specific):
//   1. CLI flag (only when explicitly set)
//   2. config file value
//   3. built-in default
package config

import "time"

// Config holds the v1 set of nine flag defaults loadable from
// ~/.diffah/config.yaml. Field-to-flag mapping is documented per field.
// Fields irrelevant to a given command (e.g., RetryTimes on `diff`) are
// silently ignored at ApplyTo time.
type Config struct {
	Platform      string        `mapstructure:"platform"`         // diff, bundle
	IntraLayer    string        `mapstructure:"intra-layer"`      // diff, bundle  (auto|off|required)
	Authfile      string        `mapstructure:"authfile"`         // diff, bundle, apply, unbundle
	RetryTimes    int           `mapstructure:"retry-times"`      // apply, unbundle
	RetryDelay    time.Duration `mapstructure:"retry-delay"`      // apply, unbundle (Go duration)
	ZstdLevel     int           `mapstructure:"zstd-level"`       // diff, bundle  (1..22)
	ZstdWindowLog string        `mapstructure:"zstd-window-log"`  // diff, bundle  (auto | 10..31)
	Workers       int           `mapstructure:"workers"`          // diff, bundle
	Candidates    int           `mapstructure:"candidates"`       // diff, bundle
}

// FlagNames maps every Config field to its CLI flag name. The flag name
// is the source of truth used by ApplyTo to look up flags in cobra's
// FlagSet.
var FlagNames = map[string]string{
	"Platform":      "platform",
	"IntraLayer":    "intra-layer",
	"Authfile":      "authfile",
	"RetryTimes":    "retry-times",
	"RetryDelay":    "retry-delay",
	"ZstdLevel":     "zstd-level",
	"ZstdWindowLog": "zstd-window-log",
	"Workers":       "workers",
	"Candidates":    "candidates",
}
```

- [ ] **Step 4: Create `pkg/config/defaults.go`**

```go
package config

import "time"

// Default returns the built-in default Config used when no config file is
// found. These values are the single source of truth for "no flag and no
// config" behavior; CLI flag defaults installed by individual commands
// must agree with these.
func Default() *Config {
	return &Config{
		Platform:      "linux/amd64",
		IntraLayer:    "auto",
		Authfile:      "",
		RetryTimes:    0,
		RetryDelay:    0,
		ZstdLevel:     22,
		ZstdWindowLog: "auto",
		Workers:       8,
		Candidates:    3,
	}
}
```

- [ ] **Step 5: Run test, verify pass**

```bash
go test ./pkg/config/ -run TestDefault_ContainsAllNineFields -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/config/config.go pkg/config/defaults.go pkg/config/config_test.go
git commit -m "feat(config): Config struct + built-in defaults

Nine fields covering platform/intra-layer/authfile + retry pair +
encoding tuning quad. Defaults match the values commands install
themselves today, so introducing config without a file is a no-op.

Refs: docs/superpowers/plans/2026-04-29-phase5-config.md Task 2"
```

### Task 3: Add Categorized error type

**Files:**
- Create: `pkg/config/errors.go`

- [ ] **Step 1: Create `pkg/config/errors.go`**

```go
package config

import (
	"fmt"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

// ConfigError is the sentinel error produced for any malformed config
// file content (bad YAML, unknown field, type mismatch). It surfaces as
// CategoryUser through cmd.Execute → exit code 2.
type ConfigError struct {
	Path string
	Err  error
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("config: %s: %v", e.Path, e.Err)
}

func (e *ConfigError) Unwrap() error { return e.Err }

func (*ConfigError) Category() errs.Category { return errs.CategoryUser }

func (*ConfigError) NextAction() string {
	return "fix the config file (use 'diffah config validate' to inspect) or unset $DIFFAH_CONFIG"
}

var (
	_ errs.Categorized = (*ConfigError)(nil)
	_ errs.Advised     = (*ConfigError)(nil)
)
```

- [ ] **Step 2: Build & commit**

```bash
go build ./pkg/config/
git add pkg/config/errors.go
git commit -m "feat(config): ConfigError → CategoryUser (exit 2)

cmd.Execute's existing errs.Categorized check maps any *ConfigError
returned from Load/Validate to exit code 2 with the category-prefixed
'diffah: user-error: config: <path>: <reason>' message shape.

Refs: docs/superpowers/plans/2026-04-29-phase5-config.md Task 3"
```

### Task 4: Load — missing file returns defaults

**Files:**
- Create: `pkg/config/load.go`
- Create: `pkg/config/load_test.go`

- [ ] **Step 1: Write failing test**

```go
package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoad_MissingFileReturnsDefaults(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.yaml")

	cfg, err := Load(missing)

	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Equal(t, Default(), cfg)
}
```

- [ ] **Step 2: Run, verify fail**

```bash
go test ./pkg/config/ -run TestLoad_MissingFileReturnsDefaults -v
```

Expected: `undefined: Load`.

- [ ] **Step 3: Create `pkg/config/load.go` minimal**

```go
package config

import (
	"errors"
	"io/fs"
	"os"

	"github.com/spf13/viper"
)

// Load reads the config file at path and returns the resolved Config.
// A non-existent path is not an error — Default() is returned. A file
// that exists but fails to parse, or contains unknown / wrong-typed
// fields, returns a *ConfigError (CategoryUser).
//
// Pass an empty string to skip file lookup entirely (returns defaults).
func Load(path string) (*Config, error) {
	if path == "" {
		return Default(), nil
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	} else if err != nil {
		return nil, &ConfigError{Path: path, Err: err}
	}

	v := viper.New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return nil, &ConfigError{Path: path, Err: err}
	}

	cfg := Default()
	if err := v.Unmarshal(cfg, decodeOpts); err != nil {
		return nil, &ConfigError{Path: path, Err: err}
	}
	return cfg, nil
}

// decodeOpts configures viper's Unmarshal:
//   - ErrorUnused: any key in the file not present in the struct returns an error
//   - WeaklyTypedInput: false (so "5" string for int field is rejected)
//   - DecodeHook: parse Go duration strings into time.Duration
func decodeOpts(c *viper.DecoderConfigOptions) {
	// implemented in Task 5/6/7 — for now use defaults
}
```

Note: `decodeOpts` is a placeholder; subsequent tasks fill it in.

- [ ] **Step 4: Run test, verify pass**

```bash
go test ./pkg/config/ -run TestLoad_MissingFileReturnsDefaults -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/config/load.go pkg/config/load_test.go
git commit -m "feat(config): Load returns defaults when file is absent

Missing path / empty path / fs.ErrNotExist all yield Default() with
nil error. Existing-but-unreadable yields ConfigError (CategoryUser).

Refs: docs/superpowers/plans/2026-04-29-phase5-config.md Task 4"
```

### Task 5: Load — valid YAML round-trips

**Files:**
- Modify: `pkg/config/load_test.go`
- Modify: `pkg/config/load.go` (decodeOpts)

- [ ] **Step 1: Add test case to `pkg/config/load_test.go`**

```go
func TestLoad_ValidYAMLOverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
platform: linux/arm64
intra-layer: required
zstd-level: 12
workers: 4
retry-times: 5
retry-delay: 250ms
`), 0o644))

	cfg, err := Load(path)
	require.NoError(t, err)

	require.Equal(t, "linux/arm64", cfg.Platform)
	require.Equal(t, "required", cfg.IntraLayer)
	require.Equal(t, 12, cfg.ZstdLevel)
	require.Equal(t, 4, cfg.Workers)
	require.Equal(t, 5, cfg.RetryTimes)
	require.Equal(t, 250*time.Millisecond, cfg.RetryDelay)
	// Untouched fields keep defaults:
	require.Equal(t, 3, cfg.Candidates)
	require.Equal(t, "auto", cfg.ZstdWindowLog)
}
```

Add `"os"` and `"time"` to the import block.

- [ ] **Step 2: Run test, verify fail**

`time.Duration` parsing is missing. Expected fail message mentions "RetryDelay" type mismatch or zero value.

- [ ] **Step 3: Implement `decodeOpts` in `pkg/config/load.go`**

Replace the placeholder body:

```go
// decodeOpts configures viper.Unmarshal:
//   - DecodeHook chains StringToTimeDurationHookFunc so "250ms" → time.Duration
//   - ErrorUnused = true so unknown keys raise an error (Task 7)
func decodeOpts(c *mapstructure.DecoderConfig) {
	c.ErrorUnused = true
	c.DecodeHook = mapstructure.ComposeDecodeHookFunc(
		mapstructure.StringToTimeDurationHookFunc(),
		mapstructure.StringToSliceHookFunc(","),
	)
}
```

Add imports:

```go
import (
	"errors"
	"io/fs"
	"os"

	"github.com/go-viper/mapstructure/v2"   // viper's mapstructure dep — confirm path via `go doc viper.DecodeHook`
	"github.com/spf13/viper"
)
```

Note: viper v1.x re-exports mapstructure via its own internal alias; the actual import path may be `github.com/mitchellh/mapstructure` or `github.com/go-viper/mapstructure/v2` depending on the viper minor. Use `go mod tidy` after writing to let Go resolve.

Update the Unmarshal call:

```go
if err := v.Unmarshal(cfg, decodeOpts); err != nil {
```

Replace `decodeOpts func(*viper.DecoderConfigOptions)` signature change too.

- [ ] **Step 4: `go mod tidy && go test ./pkg/config/`**

```bash
go mod tidy
go test ./pkg/config/ -run TestLoad -v
```

Expected: PASS for both load tests.

- [ ] **Step 5: Commit**

```bash
git add pkg/config/load.go pkg/config/load_test.go go.mod go.sum
git commit -m "feat(config): Load parses valid YAML into Config

DecodeHook chains StringToTimeDurationHookFunc so '250ms' parses
into time.Duration. Untouched fields keep their built-in default.

Refs: docs/superpowers/plans/2026-04-29-phase5-config.md Task 5"
```

### Task 6: Load — malformed YAML returns ConfigError

**Files:**
- Modify: `pkg/config/load_test.go`

- [ ] **Step 1: Add test**

```go
func TestLoad_MalformedYAMLReturnsConfigError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("platform: [bad nested"), 0o644))

	cfg, err := Load(path)

	require.Nil(t, cfg)
	var ce *ConfigError
	require.ErrorAs(t, err, &ce)
	require.Equal(t, path, ce.Path)
}
```

- [ ] **Step 2: Run test**

```bash
go test ./pkg/config/ -run TestLoad_MalformedYAMLReturnsConfigError -v
```

Expected: PASS — already covered by Load's existing `&ConfigError{Path:path, Err:err}` wrap of `ReadInConfig` failure. If test fails, fix Load.

- [ ] **Step 3: Commit (test only — no code change expected)**

```bash
git add pkg/config/load_test.go
git commit -m "test(config): malformed YAML produces ConfigError

Confirms ReadInConfig errors are wrapped with the file path so the
'diffah: user-error: config: <path>: ...' message includes the
offending file.

Refs: docs/superpowers/plans/2026-04-29-phase5-config.md Task 6"
```

### Task 7: Load — unknown field returns ConfigError

**Files:**
- Modify: `pkg/config/load_test.go`

- [ ] **Step 1: Add test**

```go
func TestLoad_UnknownFieldReturnsConfigError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unknown.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
platform: linux/amd64
nonexistent-field: value
`), 0o644))

	cfg, err := Load(path)

	require.Nil(t, cfg)
	var ce *ConfigError
	require.ErrorAs(t, err, &ce)
	require.Contains(t, err.Error(), "nonexistent-field")
}
```

- [ ] **Step 2: Run test**

```bash
go test ./pkg/config/ -run TestLoad_UnknownFieldReturnsConfigError -v
```

Expected: PASS — `ErrorUnused: true` from Task 5 covers this. Confirm the error message references the unknown key. If not, adjust decodeOpts.

- [ ] **Step 3: Commit**

```bash
git add pkg/config/load_test.go
git commit -m "test(config): unknown field produces ConfigError

ErrorUnused = true on the mapstructure DecoderConfig means a typo
in the config file is fatal, not silently ignored. Error message
includes the offending key for actionability.

Refs: docs/superpowers/plans/2026-04-29-phase5-config.md Task 7"
```

### Task 8: Load — type mismatch returns ConfigError

**Files:**
- Modify: `pkg/config/load_test.go`

- [ ] **Step 1: Add test**

```go
func TestLoad_TypeMismatchReturnsConfigError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wrong-type.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
zstd-level: "not-a-number"
`), 0o644))

	cfg, err := Load(path)

	require.Nil(t, cfg)
	var ce *ConfigError
	require.ErrorAs(t, err, &ce)
}
```

- [ ] **Step 2: Run test**

Expected: PASS. The default mapstructure behavior (no `WeaklyTypedInput`) rejects `"not-a-number"` for an `int` field.

- [ ] **Step 3: Commit**

```bash
git add pkg/config/load_test.go
git commit -m "test(config): type mismatch produces ConfigError

Strict typing — string \"not-a-number\" for int zstd-level fails
fast rather than silently coercing to 0.

Refs: docs/superpowers/plans/2026-04-29-phase5-config.md Task 8"
```

### Task 9: Validate — wraps Load and discards struct

**Files:**
- Create: `pkg/config/validate.go`
- Create: `pkg/config/validate_test.go`

- [ ] **Step 1: Write test**

```go
package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidate_OKForMissingFile(t *testing.T) {
	require.NoError(t, Validate(filepath.Join(t.TempDir(), "absent.yaml")))
}

func TestValidate_OKForValidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ok.yaml")
	require.NoError(t, os.WriteFile(path, []byte("platform: linux/amd64\n"), 0o644))
	require.NoError(t, Validate(path))
}

func TestValidate_FailsForMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("not: valid: yaml: ["), 0o644))
	var ce *ConfigError
	require.ErrorAs(t, Validate(path), &ce)
}
```

- [ ] **Step 2: Implement `pkg/config/validate.go`**

```go
package config

// Validate parses the config file at path and discards the result. It
// returns the same errors Load does (in particular *ConfigError). A
// missing file is not an error.
//
// Used by `diffah config validate` and by `diffah doctor`'s config
// check (Phase 5.1).
func Validate(path string) error {
	_, err := Load(path)
	return err
}
```

- [ ] **Step 3: Run, verify pass, commit**

```bash
go test ./pkg/config/ -run TestValidate -v
git add pkg/config/validate.go pkg/config/validate_test.go
git commit -m "feat(config): Validate(path) thin wrapper over Load

Used by 'diffah config validate' subcommand and by Phase 5.1
doctor's config check. Same error semantics as Load.

Refs: docs/superpowers/plans/2026-04-29-phase5-config.md Task 9"
```

---

## Phase 2 — ApplyTo + cobra wiring

### Task 10: ApplyTo sets defaults for unchanged flags

**Files:**
- Create: `pkg/config/apply.go`
- Create: `pkg/config/apply_test.go`

- [ ] **Step 1: Write test**

```go
package config

import (
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

func newFlagSet() *pflag.FlagSet {
	f := pflag.NewFlagSet("test", pflag.ContinueOnError)
	f.String("platform", "linux/amd64", "")
	f.String("intra-layer", "auto", "")
	f.String("authfile", "", "")
	f.Int("retry-times", 0, "")
	f.Duration("retry-delay", 0, "")
	f.Int("zstd-level", 22, "")
	f.String("zstd-window-log", "auto", "")
	f.Int("workers", 8, "")
	f.Int("candidates", 3, "")
	return f
}

func TestApplyTo_OverridesDefaultWhenFlagNotChanged(t *testing.T) {
	f := newFlagSet()
	cfg := Default()
	cfg.Platform = "linux/arm64"
	cfg.Workers = 4

	require.NoError(t, ApplyTo(f, cfg))

	got, _ := f.GetString("platform")
	require.Equal(t, "linux/arm64", got)
	gotW, _ := f.GetInt("workers")
	require.Equal(t, 4, gotW)
}

func TestApplyTo_PreservesExplicitFlag(t *testing.T) {
	f := newFlagSet()
	require.NoError(t, f.Parse([]string{"--platform=linux/explicit"}))

	cfg := Default()
	cfg.Platform = "linux/from-config" // should NOT win

	require.NoError(t, ApplyTo(f, cfg))

	got, _ := f.GetString("platform")
	require.Equal(t, "linux/explicit", got)
}

func TestApplyTo_IgnoresFlagsNotPresent(t *testing.T) {
	// A FlagSet that only has 'platform' (e.g., the diff command
	// before retry-times was wired) must not error when ApplyTo
	// encounters Config fields with no matching flag.
	f := pflag.NewFlagSet("partial", pflag.ContinueOnError)
	f.String("platform", "linux/amd64", "")

	cfg := Default()
	cfg.Platform = "linux/arm64"
	cfg.RetryTimes = 99 // no flag for this in `f` — must be silently skipped

	require.NoError(t, ApplyTo(f, cfg))

	got, _ := f.GetString("platform")
	require.Equal(t, "linux/arm64", got)
}
```

- [ ] **Step 2: Implement `pkg/config/apply.go`**

```go
package config

import (
	"fmt"
	"reflect"
	"time"

	"github.com/spf13/pflag"
)

// ApplyTo overwrites the default value of every flag in flags whose
// name appears in FlagNames AND whose value has NOT been explicitly set
// on the command line (flag.Changed == false). Flags that are present
// in cfg but missing from flags are silently skipped — this is how
// commands consume only the subset of fields they care about.
//
// CLI flag override is preserved by the Changed check: a user who
// passed --workers=2 keeps 2, regardless of what the config file said.
func ApplyTo(flags *pflag.FlagSet, cfg *Config) error {
	if flags == nil || cfg == nil {
		return nil
	}
	cfgVal := reflect.ValueOf(cfg).Elem()
	cfgType := cfgVal.Type()

	for i := 0; i < cfgType.NumField(); i++ {
		fieldName := cfgType.Field(i).Name
		flagName, ok := FlagNames[fieldName]
		if !ok {
			continue
		}
		flag := flags.Lookup(flagName)
		if flag == nil {
			continue
		}
		if flag.Changed {
			continue
		}
		if err := setFlagFromCfg(flag, cfgVal.Field(i)); err != nil {
			return fmt.Errorf("apply config field %s to flag --%s: %w", fieldName, flagName, err)
		}
	}
	return nil
}

func setFlagFromCfg(flag *pflag.Flag, cfgVal reflect.Value) error {
	switch cfgVal.Kind() {
	case reflect.String:
		return flag.Value.Set(cfgVal.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		// time.Duration is reflect.Int64 too — but it has its own Set semantics
		if cfgVal.Type() == reflect.TypeOf(time.Duration(0)) {
			return flag.Value.Set(time.Duration(cfgVal.Int()).String())
		}
		return flag.Value.Set(fmt.Sprintf("%d", cfgVal.Int()))
	default:
		return fmt.Errorf("unsupported field kind %s", cfgVal.Kind())
	}
}
```

- [ ] **Step 3: Run, verify pass**

```bash
go test ./pkg/config/ -run TestApplyTo -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/config/apply.go pkg/config/apply_test.go
git commit -m "feat(config): ApplyTo sets unchanged flags from config

Walks every Config field, looks up its flag name in FlagNames, and
overwrites the flag's value via Set() — but only when flag.Changed
is false. Flags absent from the FlagSet are silently skipped so each
command consumes only its own fields.

Refs: docs/superpowers/plans/2026-04-29-phase5-config.md Task 10"
```

### Task 11: Wire config into root PersistentPreRunE

**Files:**
- Modify: `cmd/root.go`

- [ ] **Step 1: Read existing PersistentPreRunE** (lines 117-137 of cmd/root.go).

- [ ] **Step 2: Extend the hook**

Insert new logic just before the `installLogger` call:

```go
		// Phase 5.2: load config (env > home > none) and apply defaults
		// to the running command's flags. CLI-explicit flags already win
		// because ApplyTo only writes when flag.Changed is false.
		cfgPath := os.Getenv("DIFFAH_CONFIG")
		if cfgPath == "" {
			if home, err := os.UserHomeDir(); err == nil {
				cfgPath = filepath.Join(home, ".diffah", "config.yaml")
			}
		}
		cfg, err := config.Load(cfgPath)
		if err != nil {
			return err // *ConfigError → CategoryUser → exit 2
		}
		if err := config.ApplyTo(cmd.Flags(), cfg); err != nil {
			return err
		}
```

Add imports to `cmd/root.go`:
```go
import (
    ...
    "path/filepath"
    "github.com/leosocy/diffah/pkg/config"
    ...
)
```

- [ ] **Step 3: Build, run existing tests**

```bash
go build ./...
go test ./cmd/...
```

Expected: PASS — no behavior change for existing tests because the default config equals the existing flag defaults.

- [ ] **Step 4: Commit**

```bash
git add cmd/root.go
git commit -m "feat(cmd): root hook loads config + applies to all subcommands

Single point in PersistentPreRunE. Resolves \$DIFFAH_CONFIG > ~/.diffah/
config.yaml > none, then ApplyTo's the resolved Config against the
running command's FlagSet. ConfigError propagates as CategoryUser
(exit 2). Existing tests pass unchanged because Default() matches the
flag defaults installed by each command's flag installer.

Refs: docs/superpowers/plans/2026-04-29-phase5-config.md Task 11"
```

### Task 12: Integration test — config drives diff defaults

**Files:**
- Create: `cmd/config_integration_test.go`

- [ ] **Step 1: Write test**

```go
//go:build integration

package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDIFFAH_CONFIG_DrivesDryRunDefaults asserts that a config file at
// $DIFFAH_CONFIG sets defaults that show up in `diffah diff --dry-run`
// output, and that an explicit --workers flag overrides the config.
func TestDIFFAH_CONFIG_DrivesDryRunDefaults(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)

	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`
platform: linux/arm64
workers: 4
zstd-level: 12
`), 0o644))

	t.Setenv("DIFFAH_CONFIG", cfgPath)

	v1 := filepath.Join(root, "testdata/fixtures/v1_oci.tar")
	v2 := filepath.Join(root, "testdata/fixtures/v2_oci.tar")
	out := filepath.Join(tmp, "delta.tar")

	stdout, stderr, exit := runDiffahBin(t, bin,
		"--format=json",
		"diff", "--dry-run", v1, v2, out,
	)
	require.Equalf(t, 0, exit, "diff dry-run failed: %s", stderr)
	// dry-run JSON output includes resolved platform; assert it.
	require.Containsf(t, stdout, `"platform":"linux/arm64"`,
		"expected platform from config; got: %s", stdout)
	_ = strings.Contains // silence import (or remove if unused)
}
```

(Use the existing `findRepoRoot`, `integrationBinary`, `runDiffahBin` test helpers — confirm their package and signatures from `cmd/diff_registry_integration_test.go` or similar.)

- [ ] **Step 2: Run integration test**

```bash
go test -tags integration -run TestDIFFAH_CONFIG_DrivesDryRunDefaults ./cmd/...
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add cmd/config_integration_test.go
git commit -m "test(cmd): integration — DIFFAH_CONFIG drives diff dry-run

Writes a config to a temp file, points DIFFAH_CONFIG at it, runs
'diffah diff --dry-run --format=json' and asserts the resolved
platform from config appears in the output.

Refs: docs/superpowers/plans/2026-04-29-phase5-config.md Task 12"
```

---

## Phase 3 — Helper subcommands

### Task 13: `diffah config` parent command

**Files:**
- Create: `cmd/config.go`

- [ ] **Step 1: Create `cmd/config.go`**

```go
package cmd

import "github.com/spf13/cobra"

func newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage diffah's optional YAML configuration.",
		Long: `Manage the optional ~/.diffah/config.yaml file (or whatever
$DIFFAH_CONFIG points to). The config supplies defaults for nine
widely-repeated flags; CLI flags always override config.

Subcommands:
  show      — print the resolved config
  init      — write a template
  validate  — validate a single file
`,
	}
	cmd.AddCommand(newConfigShowCommand())
	cmd.AddCommand(newConfigInitCommand())
	cmd.AddCommand(newConfigValidateCommand())
	return cmd
}

func init() { rootCmd.AddCommand(newConfigCommand()) }
```

- [ ] **Step 2: Build (will fail until Task 14/15/16 land — that's OK; commit at end of phase)**

This task is part of a fan-out; do not commit yet. Move to Task 14.

### Task 14: `diffah config show`

**Files:**
- Create: `cmd/config_show.go`
- Modify: `cmd/config_test.go` (new file — first add)

- [ ] **Step 1: Create `cmd/config_show.go`**

```go
package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/leosocy/diffah/pkg/config"
)

func newConfigShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the resolved config.",
		Long: `Resolves the same lookup chain a real run uses
(\$DIFFAH_CONFIG > ~/.diffah/config.yaml > defaults) and prints
the resulting Config struct. Useful for debugging
"why is intra-layer=off in CI?".

--format=json prints JSON instead of YAML.`,
		Args: cobra.NoArgs,
		RunE: runConfigShow,
	}
}

func runConfigShow(cmd *cobra.Command, _ []string) error {
	path := os.Getenv("DIFFAH_CONFIG")
	if path == "" {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, ".diffah", "config.yaml")
		}
	}
	cfg, err := config.Load(path)
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
```

- [ ] **Step 2: Add yaml.v3 dep** (transitive via viper, but make it explicit):

```bash
go get gopkg.in/yaml.v3
```

- [ ] **Step 3: Write test in `cmd/config_test.go`**

```go
package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigShow_TextFormat(t *testing.T) {
	t.Setenv("DIFFAH_CONFIG", "")
	t.Setenv("HOME", t.TempDir()) // ensure no real config interferes

	var stdout bytes.Buffer
	rc := Run(&stdout, nil, "config", "show")
	require.Equal(t, 0, rc)

	out := stdout.String()
	require.True(t, strings.Contains(out, "platform: linux/amd64"), "got: %s", out)
}

func TestConfigShow_JSONFormat(t *testing.T) {
	t.Setenv("DIFFAH_CONFIG", "")
	t.Setenv("HOME", t.TempDir())

	var stdout bytes.Buffer
	rc := Run(&stdout, nil, "--format=json", "config", "show")
	require.Equal(t, 0, rc)

	out := stdout.String()
	require.True(t, strings.Contains(out, `"Platform":"linux/amd64"`) ||
		strings.Contains(out, `"Platform": "linux/amd64"`), "got: %s", out)
}
```

(Use the existing `Run` test helper in `cmd/`; confirm via existing tests.)

- [ ] **Step 4: Build & run unit tests**

```bash
go build ./...
go test ./cmd/ -run TestConfigShow -v
```

Expected: PASS.

### Task 15: `diffah config init`

**Files:**
- Create: `cmd/config_init.go`
- Modify: `cmd/config_test.go`

- [ ] **Step 1: Create `cmd/config_init.go`**

```go
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
```

- [ ] **Step 2: Add tests to `cmd/config_test.go`**

```go
func TestConfigInit_WritesTemplate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.yaml")

	var stdout bytes.Buffer
	rc := Run(&stdout, nil, "config", "init", path)
	require.Equal(t, 0, rc)

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(body), "platform: linux/amd64")
}

func TestConfigInit_RefusesToOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exists.yaml")
	require.NoError(t, os.WriteFile(path, []byte("# existing\n"), 0o600))

	var stderr bytes.Buffer
	rc := Run(nil, &stderr, "config", "init", path)
	require.NotEqual(t, 0, rc)
	require.Contains(t, stderr.String(), "already exists")
}

func TestConfigInit_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exists.yaml")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o600))

	rc := Run(nil, nil, "config", "init", "--force", path)
	require.Equal(t, 0, rc)

	body, _ := os.ReadFile(path)
	require.Contains(t, string(body), "platform:")
}
```

Add `"os"`, `"path/filepath"` imports as needed.

- [ ] **Step 3: Build, run tests**

```bash
go build ./...
go test ./cmd/ -run TestConfigInit -v
```

Expected: PASS.

### Task 16: `diffah config validate`

**Files:**
- Create: `cmd/config_validate.go`
- Modify: `cmd/config_test.go`

- [ ] **Step 1: Create `cmd/config_validate.go`**

```go
package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/config"
)

func newConfigValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate [PATH]",
		Short: "Validate a config file.",
		Long: `Validate a single config file. PATH defaults to the resolved
config path (\$DIFFAH_CONFIG > ~/.diffah/config.yaml). Exits 0 on
valid config (or missing file); exits 2 on parse errors.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runConfigValidate,
	}
}

func runConfigValidate(cmd *cobra.Command, args []string) error {
	path := os.Getenv("DIFFAH_CONFIG")
	if path == "" {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, ".diffah", "config.yaml")
		}
	}
	if len(args) == 1 {
		path = args[0]
	}
	if err := config.Validate(path); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "ok: %s\n", path)
	return nil
}
```

- [ ] **Step 2: Add tests to `cmd/config_test.go`**

```go
func TestConfigValidate_OKForValidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ok.yaml")
	require.NoError(t, os.WriteFile(path, []byte("platform: linux/amd64\n"), 0o644))

	rc := Run(nil, nil, "config", "validate", path)
	require.Equal(t, 0, rc)
}

func TestConfigValidate_ExitsTwoOnInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("not: valid: yaml: ["), 0o644))

	var stderr bytes.Buffer
	rc := Run(nil, &stderr, "config", "validate", path)
	require.Equal(t, 2, rc)
	require.Contains(t, stderr.String(), "config:")
}
```

- [ ] **Step 3: Build, run tests, commit Phase 3 as one commit** (parent + 3 subcommands hang together):

```bash
go build ./...
go test ./cmd/ -run TestConfigInit -v
go test ./cmd/ -run TestConfigShow -v
go test ./cmd/ -run TestConfigValidate -v
git add cmd/config.go cmd/config_show.go cmd/config_init.go cmd/config_validate.go cmd/config_test.go go.mod go.sum
git commit -m "feat(cmd): diffah config show / init / validate

Three helper subcommands per spec §5.4:
- show: prints resolved config (yaml default; --format=json for JSON)
- init [PATH]: writes a template; refuses overwrite without --force
- validate [PATH]: validates a single file; exits 2 on parse error,
  shares Validate() with the future doctor 'config' check

Refs: docs/superpowers/plans/2026-04-29-phase5-config.md Tasks 13-16"
```

---

## Phase 4 — Docs & ship

### Task 17: CHANGELOG entry

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Insert under `[Unreleased]` section**

Add a new top-level `[Unreleased] — Phase 5: DX & diagnostics polish` block at the top (above the existing `[Unreleased] — Apply correctness & resilience` block). Inside it:

```markdown
## [Unreleased] — Phase 5: DX & diagnostics polish

### Additions

- **Config file** (`~/.diffah/config.yaml` or `$DIFFAH_CONFIG`) supplies
  defaults for nine flags: `--platform`, `--intra-layer`, `--authfile`,
  `--retry-times`, `--retry-delay`, `--zstd-level`, `--zstd-window-log`,
  `--workers`, `--candidates`. CLI flags always override config; absent
  config = built-in defaults; malformed config = exit 2 with the offending
  file path and the parse error.
- New helper subcommands:
  - `diffah config show` — print the resolved config (yaml; `--format=json` for JSON).
  - `diffah config init [PATH] [--force]` — write a template.
  - `diffah config validate [PATH]` — validate a single file.

### Behavior changes

- `diff` / `bundle` / `apply` / `unbundle` flag defaults now come from the
  resolved config when no flag is set on the command line. With no config
  file present, behavior is unchanged.

### Backward compatibility

- No change for users who don't create a config file. Existing CI scripts
  with explicit flags keep their explicit values.
```

- [ ] **Step 2: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs(changelog): Phase 5.2 config file entry

Refs: docs/superpowers/plans/2026-04-29-phase5-config.md Task 17"
```

### Task 18: Final smoke + push + PR

**Files:**
- (none)

- [ ] **Step 1: Full quality gate**

```bash
go test ./...
go test -tags integration ./...
go vet ./...
golangci-lint run ./...
go build ./...
```

All must pass.

- [ ] **Step 2: Push branch**

```bash
git push -u origin spec/phase5-dx-polish
```

(Branch was created during spec writing; commit history starts at the spec.)

- [ ] **Step 3: Open PR**

```bash
gh pr create --base master --head spec/phase5-dx-polish \
  --title "feat(config): YAML config file (Phase 5.2)" \
  --body "$(cat <<'EOF'
## Summary

Phase 5.2 of the production-readiness roadmap. Adds an optional YAML
configuration file at \`~/.diffah/config.yaml\` (or \`\$DIFFAH_CONFIG\`)
that supplies defaults for nine widely-repeated flags. CLI flags always
override config; absent file = built-in defaults; malformed file =
exit 2.

- New package \`pkg/config/\` (Config struct, defaults, Load, Validate, ApplyTo).
- Backed by \`spf13/viper\` for YAML/TOML/JSON auto-detection.
  AutomaticEnv NOT enabled — only \`\$DIFFAH_CONFIG\` path override.
- New subcommands: \`diffah config show / init / validate\`.
- Cobra integration: extended root \`PersistentPreRunE\` to load + apply
  defaults to every subcommand's flag set.

## Test plan

- [x] \`go test ./pkg/config/...\` (unit: defaults + Load + Validate + ApplyTo)
- [x] \`go test ./cmd/...\` (unit: show / init / validate)
- [x] \`go test -tags integration\` (integration: \$DIFFAH_CONFIG drives diff dry-run defaults)
- [x] \`go vet ./...\`, \`golangci-lint run\`

Refs: \`docs/superpowers/specs/2026-04-29-phase5-dx-polish-design.md\` §5
Refs: \`docs/superpowers/plans/2026-04-29-phase5-config.md\`
EOF
)"
```

- [ ] **Step 4: Wait for CI, fix failures, merge**

```bash
gh pr checks <PR-NUMBER> --watch --interval 30
```

If green:
```bash
gh pr merge <PR-NUMBER> --squash --delete-branch
```

If failures: investigate locally, fix, push, repeat.

---

## Self-review checklist

(Run before declaring complete.)

**Spec coverage:**
- §5.1 schema (9 fields) → Task 2 ✔
- §5.2 lookup chain → Task 11 (root hook) ✔
- §5.3 hard-fail on parse error → Tasks 6, 7, 8 ✔
- §5.4 subcommands show/init/validate → Tasks 13–16 ✔
- §5.5 cobra integration via PersistentPreRunE → Task 11 ✔
- §5.6 package layout → Tasks 1–10 (pkg/config) + 13–16 (cmd) ✔
- §5.7 error categorization → Task 3 ✔
- §8.1 unit coverage → embedded per-task ✔
- §8.2 integration coverage → Task 12 ✔
- §9 backward compat → CHANGELOG (Task 17) + tests passing unchanged ✔
- §10 PR strategy (PR-1 first) → Task 18 ✔

**Placeholder scan:** none.

**Type consistency:** `Config` field names (Pascal) vs YAML tags (kebab) vs `FlagNames` map vs `pflag.FlagSet.Lookup` strings — all consistent (kebab in YAML / flag names; Pascal in Go).

**Open issues to verify during execution:**
- viper's mapstructure import path varies by minor version. Use `go mod tidy` after Task 5 to let Go pick the right one.
- yaml.v3 Marshal of an unexported tag may serialize fields with their Go names. If `config show` prints `Platform:` instead of `platform:`, switch to a hand-rolled marshaler that uses the `mapstructure` tags (or use `yaml:"platform"` tags duplicated alongside `mapstructure:"platform"`).
- The integration test in Task 12 assumes `dryrun.go` includes platform in JSON output. Confirm by reading `cmd/dryrun.go`'s output schema; if not present, adjust the assertion to a different observable field (e.g., `workers`).
