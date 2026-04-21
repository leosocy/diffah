package exporter

import "context"

// ExportWithFingerprinter injects a custom Fingerprinter into the export
// pipeline. It is intended solely for tests — notably the integration
// tests in pkg/importer that need to drive the intra-layer planner
// down specific branches (e.g., size-only fallback). Production code
// must use Export with the default fingerprinter; this function carries
// no stability guarantee.
func ExportWithFingerprinter(ctx context.Context, opts Options, fp Fingerprinter) error {
	opts.fingerprinter = fp
	return Export(ctx, opts)
}
