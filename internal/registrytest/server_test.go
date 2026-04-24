package registrytest_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/registrytest"
)

func TestNewAnonymousServerServesV2(t *testing.T) {
	srv := registrytest.New(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL() + "/v2/")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestWithBasicAuth_RejectsAnonymous(t *testing.T) {
	srv := registrytest.New(t, registrytest.WithBasicAuth("alice", "s3cret"))
	defer srv.Close()

	resp, err := http.Get(srv.URL() + "/v2/")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestWithBasicAuth_AcceptsCorrectCreds(t *testing.T) {
	srv := registrytest.New(t, registrytest.WithBasicAuth("alice", "s3cret"))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL()+"/v2/", nil)
	req.SetBasicAuth("alice", "s3cret")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestAccessLog_RecordsBlobFetches(t *testing.T) {
	srv := registrytest.New(t)
	defer srv.Close()

	// Touch a blob URL so the middleware records it (even as 404).
	resp, err := http.Get(srv.URL() + "/v2/some/repo/blobs/sha256:abc")
	require.NoError(t, err)
	resp.Body.Close()

	hits := srv.BlobHits()
	require.Len(t, hits, 1)
	require.Equal(t, "some/repo", hits[0].Repo)
	require.Equal(t, "sha256:abc", hits[0].Digest.String())
}
