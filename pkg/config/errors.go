package config

import (
	"fmt"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

// LoadError is the sentinel error produced for any malformed config
// file content (bad YAML, unknown field, type mismatch). It surfaces as
// CategoryUser through cmd.Execute → exit code 2.
type LoadError struct {
	Path string
	Err  error
}

func (e *LoadError) Error() string {
	return fmt.Sprintf("config: %s: %v", e.Path, e.Err)
}

func (e *LoadError) Unwrap() error { return e.Err }

func (*LoadError) Category() errs.Category { return errs.CategoryUser }

func (*LoadError) NextAction() string {
	return "fix the config file (use 'diffah config validate' to inspect) or unset $DIFFAH_CONFIG"
}

var (
	_ errs.Categorized = (*LoadError)(nil)
	_ errs.Advised     = (*LoadError)(nil)
)
