package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

// spoolOpts holds Phase-4-streaming runtime knobs that govern where the
// exporter spills its per-Export workdir and how much encoder RAM the
// admission controller is allowed to grant. Both have sane defaults; the
// flags are advanced operator escape hatches.
type spoolOpts struct {
	Workdir      string
	MemoryBudget int64
}

type spoolOptsBuilder func() (spoolOpts, error)

const spoolHelp = `Spool & memory:
  --workdir DIR              spool location for per-Export disk-backed blobs
                             (default: <dir(OUTPUT)>/.diffah-tmp/<random>; also DIFFAH_WORKDIR env)
  --memory-budget BYTES      admission cap for concurrent encoders
                             (default: 8GiB; supports KiB/MiB/GiB/KB/MB/GB; 0 disables)
`

// installSpoolFlags registers --workdir and --memory-budget on cmd.
func installSpoolFlags(cmd *cobra.Command) spoolOptsBuilder {
	o := &spoolOpts{}
	var memStr string

	f := cmd.Flags()
	f.StringVar(&o.Workdir, "workdir", "",
		"spool location for per-Export disk-backed blobs (default <dir(OUTPUT)>/.diffah-tmp/<random>; also DIFFAH_WORKDIR)")
	f.StringVar(&memStr, "memory-budget", "8GiB",
		"admission cap for concurrent encoders; suffixes KiB/MiB/GiB/KB/MB/GB; 0 disables")

	return func() (spoolOpts, error) {
		n, err := parseMemoryBudget(memStr)
		if err != nil {
			return spoolOpts{}, &cliErr{cat: errs.CategoryUser, msg: err.Error()}
		}
		o.MemoryBudget = n
		return *o, nil
	}
}

// parseMemoryBudget accepts decimal suffixes (KB/MB/GB) and binary
// suffixes (KiB/MiB/GiB), case-insensitive on the suffix. "0" disables
// admission control. Bare integer means bytes.
func parseMemoryBudget(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("--memory-budget must not be empty")
	}
	mults := []struct {
		suffix string
		mult   int64
	}{
		{"KIB", 1 << 10}, {"MIB", 1 << 20}, {"GIB", 1 << 30},
		{"KB", 1000}, {"MB", 1000 * 1000}, {"GB", 1000 * 1000 * 1000},
		{"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30},
	}
	upper := strings.ToUpper(s)
	for _, m := range mults {
		if strings.HasSuffix(upper, m.suffix) {
			body := strings.TrimSpace(upper[:len(upper)-len(m.suffix)])
			n, err := parseInt64(body)
			if err != nil || n < 0 {
				return 0, fmt.Errorf("--memory-budget %q: invalid number", s)
			}
			return n * m.mult, nil
		}
	}
	n, err := parseInt64(upper)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("--memory-budget %q: invalid (expected number with optional suffix)", s)
	}
	return n, nil
}

func parseInt64(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}
