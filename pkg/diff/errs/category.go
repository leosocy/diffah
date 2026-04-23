// Package errs classifies errors into categories that determine exit codes
// and user-facing hints. Each Category maps to a distinct exit code so that
// callers can distinguish internal bugs from user mistakes, environment
// failures, and content problems.
package errs

// Category classifies an error into one of four buckets that drive the
// process exit code and user-facing messaging.
type Category int

const (
	// CategoryInternal indicates an unexpected bug inside diffah itself.
	CategoryInternal Category = iota
	// CategoryUser indicates the caller provided invalid arguments or flags.
	CategoryUser
	// CategoryEnvironment indicates an external factor (network, filesystem,
	// auth) prevented the operation from succeeding.
	CategoryEnvironment
	// CategoryContent indicates the input data (image, bundle, sidecar) is
	// malformed or incompatible.
	CategoryContent
)

// ExitCode returns the process exit code associated with the category.
func (c Category) ExitCode() int {
	switch c {
	case CategoryInternal:
		return 1
	case CategoryUser:
		return 2
	case CategoryEnvironment:
		return 3
	case CategoryContent:
		return 4
	default:
		return 1
	}
}

// String returns a human-readable label for the category.
func (c Category) String() string {
	switch c {
	case CategoryInternal:
		return "internal"
	case CategoryUser:
		return "user"
	case CategoryEnvironment:
		return "environment"
	case CategoryContent:
		return "content"
	default:
		return "internal"
	}
}

// Categorized is implemented by errors that know their own Category.
type Categorized interface {
	Category() Category
}

// Advised is implemented by errors that can suggest a remediation step.
type Advised interface {
	NextAction() string
}
