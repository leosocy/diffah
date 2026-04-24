package diff

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassifyRegistryErr_Auth(t *testing.T) {
	upstream := fmt.Errorf("unauthorized: access to the requested resource is not authorized")
	got := ClassifyRegistryErr(upstream, "ghcr.io/org/app:v1")
	var typed *ErrRegistryAuth
	require.ErrorAs(t, got, &typed)
	require.Equal(t, "ghcr.io/org/app:v1", typed.Registry)
}

func TestClassifyRegistryErr_ManifestMissing(t *testing.T) {
	upstream := fmt.Errorf("manifest unknown: manifest for repo:v99 not found")
	got := ClassifyRegistryErr(upstream, "docker://ghcr.io/org/app:v99")
	var typed *ErrRegistryManifestMissing
	require.ErrorAs(t, got, &typed)
}

func TestClassifyRegistryErr_Network(t *testing.T) {
	upstream := &url.Error{Op: "Get", URL: "https://x", Err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}
	got := ClassifyRegistryErr(upstream, "docker://x/y:z")
	var typed *ErrRegistryNetwork
	require.ErrorAs(t, got, &typed)
}

func TestClassifyRegistryErr_ManifestInvalid(t *testing.T) {
	upstream := fmt.Errorf("manifest schema version 0 is unsupported")
	got := ClassifyRegistryErr(upstream, "docker://x/y:z")
	var typed *ErrRegistryManifestInvalid
	require.ErrorAs(t, got, &typed)
	require.Contains(t, typed.Reason, "schema")
}

func TestClassifyRegistryErr_PassesThroughUnrecognized(t *testing.T) {
	unknown := errors.New("some other thing")
	got := ClassifyRegistryErr(unknown, "docker://x/y:z")
	require.Same(t, unknown, got)
}

func TestClassifyRegistryErr_NilIsNil(t *testing.T) {
	require.NoError(t, ClassifyRegistryErr(nil, "anything"))
}
