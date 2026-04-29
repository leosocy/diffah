package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

// DefaultPath returns the resolved config file path using the lookup
// chain $DIFFAH_CONFIG > ~/.diffah/config.yaml. Returns an empty
// string when neither env var nor user home dir is available — Load
// treats empty path as "no file → use defaults."
func DefaultPath() string {
	if p := os.Getenv("DIFFAH_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".diffah", "config.yaml")
}

// Load reads the config file at path and returns the resolved Config.
// A non-existent path is not an error — Default() is returned. A file
// that exists but fails to parse, or contains unknown / wrong-typed
// fields, returns a *LoadError (CategoryUser).
//
// Pass an empty string to skip file lookup entirely (returns defaults).
func Load(path string) (*Config, error) {
	if path == "" {
		return Default(), nil
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	} else if err != nil {
		return nil, &LoadError{Path: path, Err: err}
	}

	v := viper.New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return nil, &LoadError{Path: path, Err: err}
	}

	cfg := Default()
	if err := v.Unmarshal(cfg, decodeOpts); err != nil {
		return nil, &LoadError{Path: path, Err: err}
	}
	return cfg, nil
}

// decodeOpts configures viper.Unmarshal:
//   - ErrorUnused = true so unknown keys in the file raise an error
//   - DecodeHook adds StringToTimeDurationHookFunc so "250ms" parses
//     into time.Duration (extends viper's built-in hook set)
func decodeOpts(c *mapstructure.DecoderConfig) {
	c.ErrorUnused = true
	c.DecodeHook = mapstructure.ComposeDecodeHookFunc(
		mapstructure.StringToTimeDurationHookFunc(),
	)
}
