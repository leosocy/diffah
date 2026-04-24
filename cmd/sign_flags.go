package cmd

import (
	"bufio"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff/errs"
	"github.com/leosocy/diffah/pkg/signer"
)

// signRequestBuilder materialises a signer.SignRequest (minus Payload,
// which the exporter fills in after writing the archive) from flags
// registered on the parent cobra command. The second return is "signing
// requested" — true iff --sign-key was supplied. The third is an error
// for malformed flag combinations (e.g. reserved cosign:// URIs).
type signRequestBuilder func() (signer.SignRequest, bool, error)

// installSigningFlags registers --sign-key, --sign-key-password-stdin,
// --rekor-url. Returns a closure invoked in RunE after cobra parses
// argv.
func installSigningFlags(cmd *cobra.Command) signRequestBuilder {
	var keyPath, rekorURL string
	var passphraseStdin bool
	f := cmd.Flags()
	f.StringVar(&keyPath, "sign-key", "", "private key for signing (PEM or cosign-boxed PEM)")
	f.BoolVar(&passphraseStdin, "sign-key-password-stdin", false, "read key passphrase from stdin")
	f.StringVar(&rekorURL, "rekor-url", "",
		"upload signature to this Rekor transparency log. Do not set unless your delta "+
			"identifiers are safe to publish.")

	return func() (signer.SignRequest, bool, error) {
		if keyPath == "" {
			return signer.SignRequest{}, false, nil
		}
		if strings.HasPrefix(keyPath, "cosign://") {
			return signer.SignRequest{}, false, &cliErr{
				cat:  errs.CategoryUser,
				msg:  "cosign:// KMS URIs are reserved but not yet implemented (Phase 3 supports file-path keys only)",
				hint: "use a PEM or cosign-boxed file path",
			}
		}
		req := signer.SignRequest{KeyPath: keyPath, RekorURL: rekorURL}
		if passphraseStdin {
			pass, err := readOneLine(os.Stdin)
			if err != nil {
				return signer.SignRequest{}, false, &cliErr{
					cat: errs.CategoryUser, msg: "read passphrase from stdin: " + err.Error(),
				}
			}
			req.PassphraseBytes = pass
		}
		return req, true, nil
	}
}

// readOneLine consumes bytes from r until '\n' or EOF and returns the
// line without the trailing '\n'. EOF without a newline returns any
// bytes read so far (no error) so callers can use `echo -n` safely.
func readOneLine(r io.Reader) ([]byte, error) {
	br := bufio.NewReader(r)
	buf := make([]byte, 0, 64)
	for {
		b, err := br.ReadByte()
		if errors.Is(err, io.EOF) {
			return buf, nil
		}
		if err != nil {
			return nil, err
		}
		if b == '\n' {
			return buf, nil
		}
		buf = append(buf, b)
	}
}
