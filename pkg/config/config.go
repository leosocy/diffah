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
