package exporter

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/zstdpatch"
)

func TestResolveMode_Table(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		probeOK   bool
		reason    string
		wantEff   string
		wantWarn  string
		wantErrIs error
	}{
		{"auto+ok", "auto", true, "", "auto", "", nil},
		{"empty+ok_defaults_to_auto", "", true, "", "auto", "", nil},
		{"auto+missing_downgrades", "auto", false, "zstd not on $PATH", "off",
			"diffah: zstd not on $PATH; disabling intra-layer for this run\n", nil},
		{"off_skips_probe_even_when_missing", "off", false, "zstd not on $PATH", "off", "", nil},
		{"required+ok", "required", true, "", "auto", "", nil},
		{"required+missing_hardfails", "required", false, "zstd not on $PATH", "", "",
			zstdpatch.ErrZstdBinaryMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := func(context.Context) (bool, string) { return tc.probeOK, tc.reason }
			var warn bytes.Buffer
			eff, err := resolveMode(context.Background(), tc.input, probe, &warn)
			if tc.wantErrIs != nil {
				require.Error(t, err)
				require.True(t, errors.Is(err, tc.wantErrIs))
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantEff, eff)
			require.Equal(t, tc.wantWarn, warn.String())
		})
	}
}

func TestResolveMode_UnknownValueRejected(t *testing.T) {
	probe := func(context.Context) (bool, string) { return true, "" }
	_, err := resolveMode(context.Background(), "aggressive", probe, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--intra-layer")
	require.Contains(t, err.Error(), "aggressive")
}
