//go:build integration

package cmd_test

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/registrytest"
)

// TestDoctorProbe_OKAgainstSeededRegistry asserts a successful manifest
// fetch (network check status=ok, exit code 0).
//
// REGISTRY_AUTH_FILE is pinned to a non-existent path so the upstream
// containers-image credential lookup uses ONLY that path (ENOENT →
// anonymous) instead of falling through to /run/containers/<uid>/auth.json,
// which is present-but-unreadable on GitHub Actions runners and would
// surface as EACCES → classified by ClassifyRegistryErr as
// "authentication failed". The doctor's authfile check still returns
// "warn" (file does not exist) which does not affect the exit code.
func TestDoctorProbe_OKAgainstSeededRegistry(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t)
	seedOCIIntoRegistry(t, srv, "app/v1", filepath.Join(root, "testdata/fixtures/v1_oci.tar"), nil)

	t.Setenv("REGISTRY_AUTH_FILE", filepath.Join(t.TempDir(), "absent-auth.json"))

	stdout, stderr, exit := runDiffahBin(t, bin,
		"doctor",
		"--probe", registryDockerURL(t, srv, "app/v1"),
		"--tls-verify=false",
	)
	require.Equalf(t, 0, exit, "doctor failed: stdout=%s stderr=%s", stdout, stderr)
	require.Containsf(t, stdout, "network", "expected network check in output: %s", stdout)
	require.NotContainsf(t, stdout, "fail (", "no check should fail: %s", stdout)
}

// TestDoctorProbe_FailOn401 asserts that an unauthenticated probe
// against a Basic-auth-guarded registry causes the network check to
// fail and the process to exit 3 (CategoryEnvironment).
func TestDoctorProbe_FailOn401(t *testing.T) {
	bin := integrationBinary(t)
	srv := registrytest.New(t, registrytest.WithBasicAuth("alice", "s3cret"))

	stdout, stderr, exit := runDiffahBin(t, bin,
		"doctor",
		"--probe", registryDockerURL(t, srv, "private/foo"),
		"--tls-verify=false",
		"--no-creds",
	)
	require.Equalf(t, 3, exit, "expected exit 3 (env): stdout=%s stderr=%s", stdout, stderr)
	require.Containsf(t, stdout, "network", "expected network check: %s", stdout)
	out401 := strings.ToLower(stdout)
	require.Truef(t,
		strings.Contains(out401, "unauthorized") ||
			strings.Contains(out401, "authentication failed"),
		"expected auth failure: %s", stdout)
}

// TestDoctorProbe_FailOnManifestMissing asserts a probe against a
// non-existent tag produces a manifest-missing classification.
func TestDoctorProbe_FailOnManifestMissing(t *testing.T) {
	bin := integrationBinary(t)
	srv := registrytest.New(t)

	stdout, stderr, exit := runDiffahBin(t, bin,
		"doctor",
		"--probe", registryDockerURL(t, srv, "does-not-exist/at-all"),
		"--tls-verify=false",
	)
	require.Equalf(t, 3, exit, "expected exit 3: stdout=%s stderr=%s", stdout, stderr)
	require.Containsf(t, stdout, "network", "expected network check: %s", stdout)
	out := strings.ToLower(stdout)
	require.Truef(t,
		strings.Contains(out, "manifest unknown") ||
			strings.Contains(out, "name unknown") ||
			strings.Contains(out, "not found"),
		"expected manifest-missing classification: %s", stdout)
}

// TestDoctorProbe_TimeoutOnBlackHole asserts that a registry that
// accepts the TCP connection but never returns a response causes the
// network check to fail within ~15 s, not hang indefinitely.
//
// The black-hole listener calls Accept and holds connections open
// without writing. Go's HTTP client will block on the read, and our
// context.WithTimeout(probeTimeout) must cancel the request.
func TestDoctorProbe_TimeoutOnBlackHole(t *testing.T) {
	bin := integrationBinary(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	// Goroutines exit on listener close (outer Accept loop) or on the
	// subprocess closing its connection (per-conn Read returns EOF).
	// t.Cleanup runs after the subprocess has exited, so the chain
	// unwinds without a leak.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1024)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
				}
			}(conn)
		}
	}()

	addr := ln.Addr().String()
	probe := "docker://" + addr + "/foo:tag"

	start := time.Now()
	stdout, stderr, exit := runDiffahBin(t, bin,
		"doctor",
		"--probe", probe,
		"--tls-verify=false",
	)
	elapsed := time.Since(start)

	require.Equalf(t, 3, exit, "expected exit 3: stdout=%s stderr=%s", stdout, stderr)
	require.Lessf(t, elapsed, 25*time.Second,
		"doctor took %s — must abort within ~15 s + grace", elapsed)
	require.Containsf(t, stdout, "network", "expected network check: %s", stdout)
	out := strings.ToLower(stdout)
	require.Truef(t,
		strings.Contains(out, "deadline") ||
			strings.Contains(out, "context") ||
			strings.Contains(out, "timeout") ||
			strings.Contains(out, "canceled"),
		"expected timeout/deadline classification: %s", stdout)
}
