package config

import "time"

// Default returns the built-in default Config used when no config file is
// found. These values are the single source of truth for "no flag and no
// config" behavior; CLI flag defaults installed by individual commands
// must agree with these.
func Default() *Config {
	return &Config{
		Platform:          "linux/amd64",
		IntraLayer:        "auto",
		Authfile:          "",
		RetryTimes:        3,
		RetryDelay:        time.Duration(0),
		ZstdLevel:         22,
		ZstdWindowLog:     "auto",
		Workers:           8,
		Candidates:        3,
		Workdir:           "",     // empty = <dir(OUTPUT)>/.diffah-tmp/<random> at Export time
		MemoryBudget:      "8GiB", // matches --memory-budget cobra flag default
		ApplyWorkdir:      "",     // empty = <dir(OUTPUT)>/.diffah-tmp/<random> at Import time
		ApplyMemoryBudget: "8GiB", // matches --memory-budget cobra flag default for apply/unbundle
		ApplyWorkers:      8,
	}
}
