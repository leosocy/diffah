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
// ~/.diffah/config.yaml. Field-to-flag mapping is documented per field.
// Fields irrelevant to a given command (e.g., RetryTimes on `diff`) are
// silently ignored at ApplyTo time.
//
// Both mapstructure and yaml tags use the same kebab-case key names so
// that `config init` output round-trips through `config validate` without
// error (yaml.Marshal uses yaml tags; viper uses mapstructure tags).
type Config struct {
	Platform      string        `mapstructure:"platform"        yaml:"platform"`          // diff, bundle
	IntraLayer    string        `mapstructure:"intra-layer"     yaml:"intra-layer"`       // diff, bundle (auto|off|required)
	Authfile      string        `mapstructure:"authfile"        yaml:"authfile"`          // diff, bundle, apply, unbundle
	RetryTimes    int           `mapstructure:"retry-times"     yaml:"retry-times"`       // apply, unbundle
	RetryDelay    time.Duration `mapstructure:"retry-delay"     yaml:"retry-delay"`       // apply, unbundle (Go duration)
	ZstdLevel     int           `mapstructure:"zstd-level"      yaml:"zstd-level"`        // diff, bundle  (1..22)
	ZstdWindowLog string        `mapstructure:"zstd-window-log" yaml:"zstd-window-log"`   // diff, bundle  (auto | 10..31)
	Workers       int           `mapstructure:"workers"         yaml:"workers"`           // diff, bundle
	Candidates    int           `mapstructure:"candidates"      yaml:"candidates"`        // diff, bundle
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
