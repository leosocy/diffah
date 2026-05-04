// Package config loads diffah's optional YAML configuration file.
//
// Lookup order (first match wins for the file path):
//  1. $DIFFAH_CONFIG (must be absolute path)
//  2. ~/.diffah/config.yaml
//  3. (no file → built-in defaults)
//
// Per-field precedence (most → least specific):
//  1. CLI flag (only when explicitly set)
//  2. config file value
//  3. built-in default
package config

import (
	"reflect"
	"time"
)

// Config holds the v1 set of nine flag defaults loadable from
// ~/.diffah/config.yaml. Per-command applicability is documented in
// the design spec §5.1; fields irrelevant to a given command (e.g.,
// RetryTimes on `diff`) are silently ignored at ApplyTo time.
//
// All three tag families use the same kebab-case key so `config init`
// output round-trips through `config validate` (yaml.Marshal uses yaml
// tags; viper uses mapstructure tags; encoding/json uses json tags).
type Config struct {
	Platform      string        `mapstructure:"platform" yaml:"platform" json:"platform"`
	IntraLayer    string        `mapstructure:"intra-layer" yaml:"intra-layer" json:"intra-layer"`
	Authfile      string        `mapstructure:"authfile" yaml:"authfile" json:"authfile"`
	RetryTimes    int           `mapstructure:"retry-times" yaml:"retry-times" json:"retry-times"`
	RetryDelay    time.Duration `mapstructure:"retry-delay" yaml:"retry-delay" json:"retry-delay"`
	ZstdLevel     int           `mapstructure:"zstd-level" yaml:"zstd-level" json:"zstd-level"`
	ZstdWindowLog string        `mapstructure:"zstd-window-log" yaml:"zstd-window-log" json:"zstd-window-log"`
	Workers       int           `mapstructure:"workers" yaml:"workers" json:"workers"`
	Candidates    int           `mapstructure:"candidates" yaml:"candidates" json:"candidates"`
	Workdir       string        `mapstructure:"workdir" yaml:"workdir" json:"workdir"`
	MemoryBudget  string        `mapstructure:"memory-budget" yaml:"memory-budget" json:"memory-budget"`
	// Apply-side streaming I/O knobs. Mirror the export-side workdir/memory-budget
	// but kept under apply-* keys so each command line and config file can be
	// tuned independently.
	ApplyWorkdir      string `mapstructure:"apply-workdir" yaml:"apply-workdir" json:"apply-workdir"`
	ApplyMemoryBudget string `mapstructure:"apply-memory-budget" yaml:"apply-memory-budget" json:"apply-memory-budget"`
	ApplyWorkers      int    `mapstructure:"apply-workers" yaml:"apply-workers" json:"apply-workers"`
}

// flagNames is the Go-field-name → CLI-flag-name lookup used by
// ApplyTo. It is derived from mapstructure struct tags at init time
// so the struct definition is the single source of truth.
var flagNames map[string]string

func init() {
	flagNames = make(map[string]string)
	t := reflect.TypeOf(Config{})
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if tag := f.Tag.Get("mapstructure"); tag != "" {
			flagNames[f.Name] = tag
		}
	}
}
