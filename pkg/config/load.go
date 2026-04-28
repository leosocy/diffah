package config

import (
	"errors"
	"io/fs"
	"os"

	"github.com/go-viper/mapstructure/v2"
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
