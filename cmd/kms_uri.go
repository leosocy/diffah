package cmd

import (
	"strings"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

// rejectKMSURI guards a key path against the reserved-but-deferred
// cosign:// scheme. The Phase 3 spec keeps the syntax open so that a
// future KMS phase is additive rather than a CLI break. Callers pass
// "private" for --sign-key contexts and "public-key" for --verify
// contexts so the error surface is unambiguous.
func rejectKMSURI(keyPath, role string) error {
	if !strings.HasPrefix(keyPath, "cosign://") {
		return nil
	}
	return &cliErr{
		cat:  errs.CategoryUser,
		msg:  "cosign:// KMS " + role + " URIs are reserved but not yet implemented (Phase 3 supports file-path keys only)",
		hint: "use a PEM or cosign-boxed file path",
	}
}
