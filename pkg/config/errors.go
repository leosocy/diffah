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
