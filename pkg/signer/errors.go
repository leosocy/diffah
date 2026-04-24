// Package signer emits and verifies cosign-compatible keyed signatures
// over the diffah sidecar digest. See the design doc at
// docs/superpowers/specs/2026-04-24-phase3-registry-native-export-signing-design.md
// for the wire format; the public surface is intentionally narrow
// (Sign, Verify, WriteSidecars, LoadSidecars, JCSCanonical helpers).
package signer

import (
	"errors"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

// ErrKeyEncrypted indicates a key file is encrypted but no passphrase
// was supplied.
var ErrKeyEncrypted = &categorizedErr{
	msg: "private key is encrypted; provide --sign-key-password-stdin",
	cat: errs.CategoryUser,
}

// ErrKeyPassphraseIncorrect indicates the supplied passphrase did not
// decrypt the key.
var ErrKeyPassphraseIncorrect = &categorizedErr{
	msg: "private key passphrase is incorrect",
	cat: errs.CategoryUser,
}

// ErrKeyUnsupportedKDF indicates a cosign-boxed key was produced with
// KDF parameters we cannot decrypt.
var ErrKeyUnsupportedKDF = &categorizedErr{
	msg: "private key uses unsupported KDF parameters",
	cat: errs.CategoryUser,
}

// ErrSignatureInvalid indicates the cryptographic check failed.
var ErrSignatureInvalid = &categorizedErr{
	msg: "signature does not verify under the supplied public key",
	cat: errs.CategoryContent,
}

// ErrArchiveUnsigned indicates --verify was supplied but the archive
// carries no signature sidecar.
var ErrArchiveUnsigned = &categorizedErr{
	msg: "archive has no signature; --verify requires a signed archive",
	cat: errs.CategoryContent,
}

// categorizedErr is a sentinel error that carries an errs.Category so
// the CLI exit-code classifier can route signer failures to the right
// exit bucket. All exported Err* variables in this package are pointers
// to a categorizedErr so `errors.Is` works by identity.
type categorizedErr struct {
	msg string
	cat errs.Category
}

func (e *categorizedErr) Error() string           { return e.msg }
func (e *categorizedErr) Category() errs.Category { return e.cat }

// Is supports errors.Is(err, ErrFoo) for the sentinels declared above.
func (e *categorizedErr) Is(target error) bool { return errors.Is(target, e) }
