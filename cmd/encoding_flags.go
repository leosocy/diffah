package cmd

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

// encodingTuningHelp is the long-help text describing the four Phase 4
// encoding flags. Embedded into the Long: docstring on subcommands that
// install them so `diffah <verb> --help` documents the determinism
// guarantee and the override paths in one place.
const encodingTuningHelp = `Encoding tuning:
  --workers N           layers to fingerprint and encode in parallel (default 8).
                        --workers=1 reproduces strict serial encode. The output
                        archive is byte-identical regardless of --workers value.
  --candidates K        top-K baseline candidates per shipped layer (default 3).
                        Higher K shrinks the delta at the cost of CPU; --candidates=1
                        reproduces single-best-candidate behavior.
  --zstd-level N        zstd compression level 1..22 (default 22, "ultra"). Level=22
                        is significantly slower per layer than the historical default
                        of 3; --zstd-level=12 is a speed/size compromise.
  --zstd-window-log N   long-mode window in log2 bytes, or "auto" (default).
                        auto picks per-layer: ≤128 MiB → 27, ≤1 GiB → 30, >1 GiB → 31.
                        Encoder memory ≈ 2 × 2^N bytes per running encode.

Determinism: for a fixed (baseline, target, --candidates, --zstd-level,
--zstd-window-log) tuple, the produced delta archive is byte-identical
regardless of --workers.

Phase-3 byte-identical override:
  --zstd-level=3 --zstd-window-log=27 --candidates=1 --workers=1`

// encodingOpts is the flat collection of Phase 4 producer-side
// tunables a subcommand parses out of its flag set.
type encodingOpts struct {
	Workers       int
	Candidates    int
	ZstdLevel     int
	ZstdWindowLog int // 0 = auto
}

// encodingOptsBuilder validates parsed flags and yields the resolved
// encodingOpts. Validation is at the cobra layer so a malformed
// invocation exits with category=user (exit 2) before any I/O.
type encodingOptsBuilder func() (encodingOpts, error)

// installEncodingFlags registers the four Phase 4 tunables on cmd and
// returns a closure invoked from RunE. Defaults are deliberately the
// PR-1 historical values; PR-4 flips them to the build-farm-tuned set.
func installEncodingFlags(cmd *cobra.Command) encodingOptsBuilder {
	o := &encodingOpts{}
	var windowLog string

	f := cmd.Flags()
	f.IntVar(&o.Workers, "workers", 8,
		"layers to fingerprint and encode in parallel; "+
			"--workers=1 reproduces Phase-3 strict-serial encode")
	f.IntVar(&o.Candidates, "candidates", 3,
		"top-K baseline candidates per shipped layer; "+
			"--candidates=1 reproduces Phase-3 single-best behavior")
	f.IntVar(&o.ZstdLevel, "zstd-level", 22,
		"zstd compression level 1..22 ('ultra' at 22); "+
			"--zstd-level=12 is a speed/size compromise, "+
			"--zstd-level=3 matches the zstd CLI default and is the fastest")
	f.StringVar(&windowLog, "zstd-window-log", "auto",
		"zstd long-mode window as log2 bytes (10..31), "+
			"or 'auto' to pick per-layer (≤128 MiB→27, ≤1 GiB→30, >1 GiB→31)")

	return func() (encodingOpts, error) {
		if o.Workers < 1 {
			return encodingOpts{}, &cliErr{
				cat: errs.CategoryUser,
				msg: fmt.Sprintf("--workers must be >= 1, got %d", o.Workers),
			}
		}
		if o.Candidates < 1 {
			return encodingOpts{}, &cliErr{
				cat: errs.CategoryUser,
				msg: fmt.Sprintf("--candidates must be >= 1, got %d", o.Candidates),
			}
		}
		if o.ZstdLevel < 1 || o.ZstdLevel > 22 {
			return encodingOpts{}, &cliErr{
				cat: errs.CategoryUser,
				msg: fmt.Sprintf("--zstd-level must be in [1,22], got %d", o.ZstdLevel),
			}
		}
		if windowLog == "auto" {
			o.ZstdWindowLog = 0 // 0 sentinel = auto
		} else {
			n, err := strconv.Atoi(windowLog)
			if err != nil || n < 10 || n > 31 {
				return encodingOpts{}, &cliErr{
					cat: errs.CategoryUser,
					msg: fmt.Sprintf("--zstd-window-log must be 'auto' or in [10,31], got %q", windowLog),
				}
			}
			o.ZstdWindowLog = n
		}
		return *o, nil
	}
}
