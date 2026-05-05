package exporter

// rssEstimateByWindowLog is the windowLog → estimated peak RSS table.
// Values are deliberately conservative — see spec §4.3 risks. The
// admission controller blocks new encodes from being admitted unless
// (sum of in-flight estimates) + new_estimate ≤ budget.
//
// This table is exporter-specific: it captures the per-encode RSS
// envelope of zstd encoding. The pool primitive that consumes these
// estimates lives in internal/admission so the importer side can
// reuse it with its own per-task RSS model.
var rssEstimateByWindowLog = map[int]int64{
	27: 256 << 20,
	28: 512 << 20,
	29: 1 << 30,
	30: 2 << 30,
	31: 4 << 30,
}

// EstimateRSSForWindowLog returns the conservative peak RSS estimate for the
// given windowLog. Unknown values fall back to the largest table entry so that
// out-of-range inputs never under-count memory.
//
// Exported so the importer-side admission controller can reuse the same
// per-encode RSS envelope when sizing concurrent image applies (the
// patch-decode side hits the same zstd window cost as the encode side).
func EstimateRSSForWindowLog(wl int) int64 {
	if v, ok := rssEstimateByWindowLog[wl]; ok {
		return v
	}
	return rssEstimateByWindowLog[31]
}
