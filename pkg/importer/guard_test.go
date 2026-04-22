package importer

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff"
)

func TestValidatePositional_SingleOK(t *testing.T) {
	sc := &diff.Sidecar{Images: []diff.ImageEntry{{Name: "a"}}}
	require.NoError(t, validatePositionalBaseline(sc, map[string]string{"default": "b.tar"}))
}

func TestValidatePositional_MultiRejects(t *testing.T) {
	sc := &diff.Sidecar{Images: []diff.ImageEntry{{Name: "a"}, {Name: "b"}}}
	err := validatePositionalBaseline(sc, map[string]string{"default": "b.tar"})
	var e *diff.ErrMultiImageNeedsNamedBaselines
	require.ErrorAs(t, err, &e)
}

func TestValidatePositional_MultiWithNamedOK(t *testing.T) {
	sc := &diff.Sidecar{Images: []diff.ImageEntry{{Name: "a"}, {Name: "b"}}}
	err := validatePositionalBaseline(sc, map[string]string{"a": "a.tar", "b": "b.tar"})
	require.NoError(t, err)
}

func TestValidatePositional_MultiWithEmptyKeyRejects(t *testing.T) {
	sc := &diff.Sidecar{Images: []diff.ImageEntry{{Name: "a"}, {Name: "b"}}}
	err := validatePositionalBaseline(sc, map[string]string{"": "b.tar"})
	var e *diff.ErrMultiImageNeedsNamedBaselines
	require.ErrorAs(t, err, &e)
}
