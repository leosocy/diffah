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
	if err := v.Unmarshal(cfg); err != nil {
		return nil, &ConfigError{Path: path, Err: err}
	}
	return cfg, nil
}
