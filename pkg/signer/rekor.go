package signer

import (
	"context"
	"crypto/ecdsa"
	"fmt"
)

// UploadEntry POSTs a signature+payload to a Rekor instance and returns
// the resulting transparency-log bundle (cosign 2.x format). A
// zero-length rekorURL is a no-op; callers should not call UploadEntry
// unless --rekor-url was supplied.
//
// The full HTTP implementation is deferred to a follow-on PR; Phase 7's
// CLI gates this path behind --rekor-url, so the happy default never
// reaches here. Until then, any non-empty URL returns an explicit "not
// yet implemented" error, which keeps the Sign path predictable for the
// tests that exercise the no-Rekor branch.
func UploadEntry(ctx context.Context, rekorURL string, sig, payload []byte, pub *ecdsa.PublicKey) ([]byte, error) {
	_ = ctx
	_ = sig
	_ = payload
	_ = pub
	if rekorURL == "" {
		return nil, nil
	}
	return nil, fmt.Errorf("rekor upload not implemented in this phase; omit --rekor-url to proceed")
}
