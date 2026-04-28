package config

// Validate parses the config file at path and discards the result. It
// returns the same errors Load does (in particular *ConfigError). A
// missing file is not an error.
//
// Used by `diffah config validate` and by `diffah doctor`'s config
// check (Phase 5.1).
func Validate(path string) error {
	_, err := Load(path)
	return err
}
