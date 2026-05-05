package errs

// UserError carries a Category and a remediation hint, satisfying
// Categorized and Advised for the cmd/ exit-code mapper. Producers (e.g.,
// admission preflights in pkg/exporter and pkg/importer) construct one
// directly to surface a CategoryUser error without re-implementing the
// interfaces.
type UserError struct {
	Cat  Category
	Msg  string
	Hint string
}

var (
	_ Categorized = (*UserError)(nil)
	_ Advised     = (*UserError)(nil)
)

func (e *UserError) Error() string      { return e.Msg }
func (e *UserError) Category() Category { return e.Cat }
func (e *UserError) NextAction() string { return e.Hint }
