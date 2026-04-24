package imageio

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/types"
)

func TestBuildSystemContext_NoFlagsDefaultsToVerifyingTLS(t *testing.T) {
	sc, err := BuildSystemContext(SystemContextFlags{})
	require.NoError(t, err)
	require.NotNil(t, sc)
	require.Equal(t, types.OptionalBoolUndefined, sc.DockerInsecureSkipTLSVerify,
		"unset flag should leave TLS at containers-image default (=verify)")
}

func TestBuildSystemContext_TLSVerifyFalse(t *testing.T) {
	sc, err := BuildSystemContext(SystemContextFlags{TLSVerify: OptionalBoolPtr(false)})
	require.NoError(t, err)
	require.Equal(t, types.OptionalBoolTrue, sc.DockerInsecureSkipTLSVerify)
}

func TestBuildSystemContext_CredsSplit(t *testing.T) {
	sc, err := BuildSystemContext(SystemContextFlags{Creds: "alice:s3cret"})
	require.NoError(t, err)
	require.NotNil(t, sc.DockerAuthConfig)
	require.Equal(t, "alice", sc.DockerAuthConfig.Username)
	require.Equal(t, "s3cret", sc.DockerAuthConfig.Password)
}

func TestBuildSystemContext_UsernamePasswordPair(t *testing.T) {
	sc, err := BuildSystemContext(SystemContextFlags{Username: "bob", Password: "p"})
	require.NoError(t, err)
	require.Equal(t, "bob", sc.DockerAuthConfig.Username)
	require.Equal(t, "p", sc.DockerAuthConfig.Password)
}

func TestBuildSystemContext_UsernameWithoutPasswordFails(t *testing.T) {
	_, err := BuildSystemContext(SystemContextFlags{Username: "bob"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "--username requires --password")
}

func TestBuildSystemContext_BearerToken(t *testing.T) {
	sc, err := BuildSystemContext(SystemContextFlags{RegistryToken: "abc123"})
	require.NoError(t, err)
	require.Equal(t, "abc123", sc.DockerBearerRegistryToken)
}

func TestBuildSystemContext_CertDir(t *testing.T) {
	tmp := t.TempDir()
	sc, err := BuildSystemContext(SystemContextFlags{CertDir: tmp})
	require.NoError(t, err)
	require.Equal(t, tmp, sc.DockerCertPath)
}

func TestBuildSystemContext_AuthfileExplicit(t *testing.T) {
	tmp := t.TempDir()
	af := filepath.Join(tmp, "auth.json")
	require.NoError(t, os.WriteFile(af, []byte("{}"), 0o600))

	sc, err := BuildSystemContext(SystemContextFlags{AuthFile: af})
	require.NoError(t, err)
	require.Equal(t, af, sc.AuthFilePath)
}

func TestBuildSystemContext_MutuallyExclusiveCredsAndNoCreds(t *testing.T) {
	_, err := BuildSystemContext(SystemContextFlags{Creds: "u:p", NoCreds: true})
	require.Error(t, err)
	require.Contains(t, err.Error(), "mutually exclusive")
}

func TestBuildSystemContext_MutuallyExclusiveCredsAndUsername(t *testing.T) {
	_, err := BuildSystemContext(SystemContextFlags{Creds: "u:p", Username: "bob", Password: "q"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "mutually exclusive")
}

func TestBuildSystemContext_NoCredsWithBearer(t *testing.T) {
	_, err := BuildSystemContext(SystemContextFlags{NoCreds: true, RegistryToken: "x"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "mutually exclusive")
}

func TestBuildSystemContext_DefaultAuthfilePrecedence(t *testing.T) {
	tmp := t.TempDir()
	xdg := filepath.Join(tmp, "xdg", "containers")
	require.NoError(t, os.MkdirAll(xdg, 0o755))
	xdgFile := filepath.Join(xdg, "auth.json")
	require.NoError(t, os.WriteFile(xdgFile, []byte("{}"), 0o600))

	dockerDir := filepath.Join(tmp, "home", ".docker")
	require.NoError(t, os.MkdirAll(dockerDir, 0o755))
	dockerFile := filepath.Join(dockerDir, "config.json")
	require.NoError(t, os.WriteFile(dockerFile, []byte("{}"), 0o600))

	t.Setenv("REGISTRY_AUTH_FILE", "")
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmp, "xdg"))
	t.Setenv("HOME", filepath.Join(tmp, "home"))

	sc, err := BuildSystemContext(SystemContextFlags{})
	require.NoError(t, err)
	require.Equal(t, xdgFile, sc.AuthFilePath, "XDG file should win over docker config.json")
}

func TestBuildSystemContext_REGISTRY_AUTH_FILE_WinsOverXDG(t *testing.T) {
	tmp := t.TempDir()
	override := filepath.Join(tmp, "override.json")
	require.NoError(t, os.WriteFile(override, []byte("{}"), 0o600))

	xdg := filepath.Join(tmp, "xdg", "containers")
	require.NoError(t, os.MkdirAll(xdg, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(xdg, "auth.json"), []byte("{}"), 0o600))

	t.Setenv("REGISTRY_AUTH_FILE", override)
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmp, "xdg"))

	sc, err := BuildSystemContext(SystemContextFlags{})
	require.NoError(t, err)
	require.Equal(t, override, sc.AuthFilePath)
}
