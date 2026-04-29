package imageio

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.podman.io/image/v5/types"
)

// SystemContextFlags holds the raw CLI-flag values that
// BuildSystemContext translates into an upstream *types.SystemContext.
// The consumer (cmd/registry_flags.go) registers these as cobra flags
// and fills the struct.
type SystemContextFlags struct {
	AuthFile      string
	Creds         string
	Username      string
	Password      string
	NoCreds       bool
	RegistryToken string
	TLSVerify     *bool // nil = default; true/false = user override
	CertDir       string
	RetryTimes    int
	RetryDelay    time.Duration
}

// OptionalBoolPtr returns a pointer to v. Used when constructing
// SystemContextFlags to distinguish "flag not set" (nil) from explicit
// true/false.
func OptionalBoolPtr(v bool) *bool { return &v }

// BuildSystemContext validates the flag combination and constructs
// the upstream types.SystemContext.
//
// On validation failure the returned error is a plain value
// (no classification). Callers in cmd/registry_flags.go wrap it in
// a cliErr with categoryUser.
func BuildSystemContext(f SystemContextFlags) (*types.SystemContext, error) {
	if err := validateCredentialFlags(f); err != nil {
		return nil, err
	}
	sc := &types.SystemContext{}
	applyAuthFile(sc, f)
	if err := applyCredentials(sc, f); err != nil {
		return nil, err
	}
	if f.RegistryToken != "" {
		sc.DockerBearerRegistryToken = f.RegistryToken
	}
	if f.TLSVerify != nil {
		if *f.TLSVerify {
			sc.DockerInsecureSkipTLSVerify = types.OptionalBoolFalse
		} else {
			sc.DockerInsecureSkipTLSVerify = types.OptionalBoolTrue
		}
	}
	if f.CertDir != "" {
		sc.DockerCertPath = f.CertDir
	}
	return sc, nil
}

func applyAuthFile(sc *types.SystemContext, f SystemContextFlags) {
	switch {
	case f.AuthFile != "":
		sc.AuthFilePath = f.AuthFile
	case !f.NoCreds && f.Creds == "" && f.Username == "" && f.RegistryToken == "":
		sc.AuthFilePath = ResolveAuthFile()
	}
}

func applyCredentials(sc *types.SystemContext, f SystemContextFlags) error {
	switch {
	case f.Creds != "":
		user, pass, ok := splitCreds(f.Creds)
		if !ok {
			return fmt.Errorf("invalid --creds %q: expected USER[:PASS]", f.Creds)
		}
		sc.DockerAuthConfig = &types.DockerAuthConfig{Username: user, Password: pass}
	case f.Username != "":
		if f.Password == "" {
			return fmt.Errorf("--username requires --password")
		}
		sc.DockerAuthConfig = &types.DockerAuthConfig{Username: f.Username, Password: f.Password}
	case f.NoCreds:
		sc.AuthFilePath = ""
		sc.DockerAuthConfig = nil
	}
	return nil
}

func validateCredentialFlags(f SystemContextFlags) error {
	credSources := 0
	if f.Creds != "" {
		credSources++
	}
	if f.Username != "" || f.Password != "" {
		credSources++
	}
	if f.RegistryToken != "" {
		credSources++
	}
	if f.NoCreds {
		credSources++
	}
	if credSources > 1 {
		return fmt.Errorf("--creds, --username/--password, --registry-token, and --no-creds are mutually exclusive")
	}
	return nil
}

func splitCreds(raw string) (user, pass string, ok bool) {
	idx := strings.Index(raw, ":")
	if idx < 0 {
		return raw, "", raw != ""
	}
	return raw[:idx], raw[idx+1:], idx > 0
}

// ResolveAuthFile returns the first existing file in the standard
// containers-image precedence chain:
//
//  1. $REGISTRY_AUTH_FILE
//  2. $XDG_RUNTIME_DIR/containers/auth.json
//  3. $HOME/.docker/config.json
//
// Returns an empty string when none of the candidates exist (upstream
// containers-image treats this as "no credentials available"). Callers
// outside this package use this for diagnostic display (e.g., diffah
// doctor's authfile check); callers inside the package use it to seed
// SystemContext.AuthFilePath.
func ResolveAuthFile() string {
	if v := os.Getenv("REGISTRY_AUTH_FILE"); v != "" {
		if fileExists(v) {
			return v
		}
	}
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		candidate := filepath.Join(xdg, "containers", "auth.json")
		if fileExists(candidate) {
			return candidate
		}
	}
	if home := os.Getenv("HOME"); home != "" {
		candidate := filepath.Join(home, ".docker", "config.json")
		if fileExists(candidate) {
			return candidate
		}
	}
	return ""
}

// fileExists returns true when path names an existing filesystem entry.
// The path comes from environment-variable-driven config discovery
// ($REGISTRY_AUTH_FILE / $XDG_RUNTIME_DIR / $HOME); we only need to
// learn whether a candidate exists before handing it to the upstream
// containers-image library. Stat is side-effect-free, so gosec G703's
// path-traversal concern does not apply to this lookup site.
func fileExists(path string) bool {
	_, err := os.Stat(path) //nolint:gosec // G703: env-derived config path; stat-only, no content read
	return err == nil
}
