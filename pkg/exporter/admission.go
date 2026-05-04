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

// estimateRSSForWindowLog returns the conservative peak RSS estimate for the
// given windowLog. Unknown values fall back to the largest table entry so that
// out-of-range inputs never under-count memory.
func estimateRSSForWindowLog(wl int) int64 {
	if v, ok := rssEstimateByWindowLog[wl]; ok {
		return v
	}
	return rssEstimateByWindowLog[31]
}
