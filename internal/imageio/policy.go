package imageio

import (
	"fmt"

	"go.podman.io/image/v5/signature"
)

// DefaultPolicyContext returns a signature-policy context that accepts any
// image signature. v1 of diffah does not verify signatures; the caller is
// responsible for releasing the context via Destroy.
func DefaultPolicyContext() (*signature.PolicyContext, error) {
	policy := &signature.Policy{
		Default: signature.PolicyRequirements{signature.NewPRInsecureAcceptAnything()},
	}
	pc, err := signature.NewPolicyContext(policy)
	if err != nil {
		return nil, fmt.Errorf("build signature policy context: %w", err)
	}
	return pc, nil
}
