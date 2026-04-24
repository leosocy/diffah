package cmd

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

func TestParseImageRef_AcceptsSupportedTransports(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		wantT string
		wantP string
	}{
		{"docker-archive", "docker-archive:/tmp/x.tar", "docker-archive", "/tmp/x.tar"},
		{"oci-archive", "oci-archive:/tmp/y.tar", "oci-archive", "/tmp/y.tar"},
		{"docker-archive relative", "docker-archive:x.tar", "docker-archive", "x.tar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := ParseImageRef("BASELINE-IMAGE", tc.raw)
			require.NoError(t, err)
			require.Equal(t, tc.wantT, ref.Transport)
			require.Equal(t, tc.wantP, ref.Path)
		})
	}
}

func TestParseImageRef_MissingTransport(t *testing.T) {
	_, err := ParseImageRef("BASELINE-IMAGE", "/tmp/old.tar")
	require.Error(t, err)
	msg := err.Error()
	require.Contains(t, msg, "missing transport prefix")
	require.Contains(t, msg, "BASELINE-IMAGE")
	require.Contains(t, msg, `"/tmp/old.tar"`)
	require.Contains(t, msg, "docker-archive:PATH")
	require.Contains(t, msg, "oci-archive:PATH")
	require.Contains(t, msg, "Did you mean:  docker-archive:/tmp/old.tar")
}

func TestParseImageRef_MissingTransportNoHintForNonTarExt(t *testing.T) {
	_, err := ParseImageRef("TARGET-IMAGE", "/srv/layout")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "Did you mean:")
}

func TestParseImageRef_ReservedTransports(t *testing.T) {
	cases := []string{
		"docker-daemon:img:v1",
		"containers-storage:img:v1",
		"ostree:/tmp/ostree",
		"sif:/tmp/img.sif",
		"tarball:/tmp/archive.tar",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, err := ParseImageRef("BASELINE-IMAGE", raw)
			require.Error(t, err)
			msg := err.Error()
			require.Contains(t, msg, "is reserved but not yet implemented")
			require.Contains(t, msg, "docker-archive:PATH")
			require.Contains(t, msg, "oci-archive:PATH")
		})
	}
}

func TestParseImageRef_AcceptsRegistryTransports(t *testing.T) {
	// oci: and dir: transports require the path to exist on disk during parse;
	// use a temp dir so the test is hermetic.
	tmp := t.TempDir()

	cases := []struct {
		name  string
		raw   string
		wantT string
		wantP string
	}{
		{"docker", "docker://ghcr.io/org/app:v1", "docker", "//ghcr.io/org/app:v1"},
		{"oci", "oci:" + tmp, "oci", tmp},
		{"dir", "dir:" + tmp, "dir", tmp},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := ParseImageRef("BASELINE-IMAGE", tc.raw)
			require.NoError(t, err)
			require.Equal(t, tc.wantT, ref.Transport)
			require.Equal(t, tc.wantP, ref.Path)
			require.Equal(t, tc.raw, ref.Raw)
		})
	}
}

func TestParseImageRef_RejectsInvalidSyntaxForSupportedTransport(t *testing.T) {
	// "docker://" has an empty path component — alltransports.ParseImageName will reject it.
	_, err := ParseImageRef("BASELINE-IMAGE", "docker://")
	require.Error(t, err)
	msg := err.Error()
	require.True(t,
		strings.Contains(msg, "transport reference syntax") ||
			strings.Contains(msg, "invalid BASELINE-IMAGE"),
		"expected syntax error hint, got: %s", msg)
	cat, _ := errs.Classify(err)
	require.Equal(t, errs.CategoryUser, cat)
}

func TestParseImageRef_EmptyPath(t *testing.T) {
	_, err := ParseImageRef("BASELINE-IMAGE", "docker-archive:")
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "empty path") ||
		strings.Contains(err.Error(), "empty"))
}

func TestParseImageRef_UnsupportedTransport(t *testing.T) {
	cases := []struct {
		raw       string
		transport string
	}{
		{"foobar:/tmp/x.tar", "foobar"},
		{"foo:/tmp/bar", "foo"},
		{"random-transport:something", "random-transport"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			_, err := ParseImageRef("BASELINE-IMAGE", tc.raw)
			require.Error(t, err)
			msg := err.Error()
			require.Contains(t, msg, fmt.Sprintf("transport %q", tc.transport))
			require.Contains(t, msg, "not supported")
			require.Contains(t, msg, "docker-archive:PATH")
			require.Contains(t, msg, "oci-archive:PATH")
		})
	}
}

func TestParseImageRef_EmptyString(t *testing.T) {
	_, err := ParseImageRef("BASELINE-IMAGE", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing transport prefix")
}

func TestParseImageRef_ClassifyReturnsCategoryUser(t *testing.T) {
	_, err := ParseImageRef("BASELINE-IMAGE", "/tmp/old.tar")
	require.Error(t, err)
	cat, _ := errs.Classify(err)
	require.Equal(t, errs.CategoryUser, cat)
}

func TestParseImageRef_MissingTransportNextAction(t *testing.T) {
	_, err := ParseImageRef("BASELINE-IMAGE", "/tmp/archive.tar")
	require.Error(t, err)
	_, hint := errs.Classify(err)
	require.Contains(t, hint, "docker-archive:")
}

func TestParseImageRef_ReservedTransportNextAction(t *testing.T) {
	_, err := ParseImageRef("BASELINE-IMAGE", "docker-daemon:img:v1")
	require.Error(t, err)
	_, hint := errs.Classify(err)
	require.NotEmpty(t, hint)
}

func TestParseImageRef_DidYouMeanTgz(t *testing.T) {
	_, err := ParseImageRef("BASELINE-IMAGE", "/tmp/archive.tgz")
	require.Error(t, err)
	require.Contains(t, err.Error(), "Did you mean:  docker-archive:/tmp/archive.tgz")
}

func TestParseImageRef_DidYouMeanTarGz(t *testing.T) {
	_, err := ParseImageRef("BASELINE-IMAGE", "/tmp/archive.tar.gz")
	require.Error(t, err)
	require.Contains(t, err.Error(), "Did you mean:  docker-archive:/tmp/archive.tar.gz")
}
