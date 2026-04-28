package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDefault_ContainsAllNineFields(t *testing.T) {
	d := Default()

	require.Equal(t, "linux/amd64", d.Platform)
	require.Equal(t, "auto", d.IntraLayer)
	require.Equal(t, "", d.Authfile) // empty = use lookup chain
	require.Equal(t, 0, d.RetryTimes)
	require.Equal(t, time.Duration(0), d.RetryDelay)
	require.Equal(t, 22, d.ZstdLevel)
	require.Equal(t, "auto", d.ZstdWindowLog)
	require.Equal(t, 8, d.Workers)
	require.Equal(t, 3, d.Candidates)
}
