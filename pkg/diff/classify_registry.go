package diff

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// ClassifyRegistryErr maps upstream registry errors to diffah typed
// error types that carry the correct exit-code category. When the
// error is not recognised as registry-related, the original error is
// returned unchanged so the existing errs.Classify fallbacks still
// apply.
func ClassifyRegistryErr(err error, ref string) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())

	switch {
	case containsAny(msg, "unauthorized", "authentication required", "denied"):
		return &ErrRegistryAuth{Registry: ref}
	case containsAny(msg, "manifest unknown", "not found"):
		return &ErrRegistryManifestMissing{Ref: ref}
	case containsAny(msg, "schema version", "unsupported media type", "invalid manifest"):
		return &ErrRegistryManifestInvalid{Ref: ref, Reason: err.Error()}
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return &ErrRegistryNetwork{Op: urlErr.Op, Cause: err}
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return &ErrRegistryNetwork{Op: netErr.Op, Cause: err}
	}
	return err
}

func containsAny(haystack string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}
