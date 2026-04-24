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

	switch {
	case f.AuthFile != "":
		sc.AuthFilePath = f.AuthFile
	case !f.NoCreds && f.Creds == "" && f.Username == "" && f.RegistryToken == "":
		sc.AuthFilePath = defaultAuthFile()
	}

	switch {
	case f.Creds != "":
		user, pass, ok := splitCreds(f.Creds)
		if !ok {
			return nil, fmt.Errorf("invalid --creds %q: expected USER[:PASS]", f.Creds)
		}
		sc.DockerAuthConfig = &types.DockerAuthConfig{Username: user, Password: pass}
	case f.Username != "":
		if f.Password == "" {
			return nil, fmt.Errorf("--username requires --password")
		}
		sc.DockerAuthConfig = &types.DockerAuthConfig{Username: f.Username, Password: f.Password}
	case f.NoCreds:
		sc.AuthFilePath = ""
		sc.DockerAuthConfig = nil
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

// defaultAuthFile returns the first existing file in the standard
// precedence chain: $REGISTRY_AUTH_FILE → $XDG_RUNTIME_DIR/containers/auth.json
// → $HOME/.docker/config.json. Returns empty string when none exist
// (upstream containers-image treats this as "no credentials available").
func defaultAuthFile() string {
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

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
