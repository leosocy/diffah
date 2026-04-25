package diff

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// HACK: string-based matching tracks the error message contracts of
// docker/podman/distribution at the time of writing. When upstream
// phrasing drifts, update the relevant slice — both classification
// (terminal categories) and retry policy (transient signals) read from
// these vars, so a single edit keeps the two policies in sync.
var (
	registryAuthNeedles    = []string{"unauthorized", "authentication required", "denied"}
	registryMissingNeedles = []string{"manifest unknown", "name unknown", "not found"}
	registryInvalidNeedles = []string{"schema version", "unsupported media type", "invalid manifest"}
	// Transient HTTP / network signals that warrant a retry. Permanent
	// failures (auth, missing, invalid) are deliberately absent — retrying
	// them wastes wall-time without changing the outcome.
	registryRetryableNeedles = []string{
		"too many requests",     // 429
		"service unavailable",   // 503
		"bad gateway",           // 502
		"gateway timeout",       // 504
		"internal server error", // 500
		"eof",
		"connection reset",
		"connection refused",
	}
)

// ClassifyRegistryErr maps upstream registry errors to diffah typed
// error types that carry the correct exit-code category. When the
// error is not recognised as registry-related, the original error is
// returned unchanged so the existing errs.Classify fallbacks still
// apply.
//
// Case ordering is deliberate: auth is checked first so that a message
// like "unauthorized: manifest not found" classifies as auth (the
// actionable root cause) rather than a missing manifest.
func ClassifyRegistryErr(err error, ref string) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())

	switch {
	case containsAny(msg, registryAuthNeedles...):
		return &ErrRegistryAuth{Registry: ref}
	case containsAny(msg, registryMissingNeedles...):
		return &ErrRegistryManifestMissing{Ref: ref}
	case containsAny(msg, registryInvalidNeedles...):
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

// IsRetryableRegistryErr reports whether err carries a transient
// upstream signal — HTTP 429/5xx, network reset, EOF, or a wrapped
// *url.Error / *net.OpError. Permanent failures (401/403 auth,
// 404 manifest, manifest schema errors) return false so retry loops
// fail fast.
func IsRetryableRegistryErr(err error) bool {
	if err == nil {
		return false
	}
	if containsAny(strings.ToLower(err.Error()), registryRetryableNeedles...) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	var netErr *net.OpError
	return errors.As(err, &netErr)
}

func containsAny(haystack string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}
