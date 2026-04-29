package config

import (
	"fmt"
	"reflect"
	"time"

	"github.com/spf13/pflag"
)

// ApplyTo overwrites the default value of every flag in flags whose
// name appears in flagNames AND whose value has NOT been explicitly set
// on the command line (flag.Changed == false). Flags that are present
// in cfg but missing from flags are silently skipped — this is how
// commands consume only the subset of fields they care about.
//
// CLI flag override is preserved by the Changed check: a user who
// passed --workers=2 keeps 2, regardless of what the config file said.
func ApplyTo(flags *pflag.FlagSet, cfg *Config) error {
	if flags == nil || cfg == nil {
		return nil
	}
	cfgVal := reflect.ValueOf(cfg).Elem()
	cfgType := cfgVal.Type()

	for i := 0; i < cfgType.NumField(); i++ {
		fieldName := cfgType.Field(i).Name
		flagName, ok := flagNames[fieldName]
		if !ok {
			continue
		}
		flag := flags.Lookup(flagName)
		if flag == nil {
			continue
		}
		if flag.Changed {
			continue
		}
		if err := setFlagFromCfg(flag, cfgVal.Field(i)); err != nil {
			return fmt.Errorf("apply config field %s to flag --%s: %w", fieldName, flagName, err)
		}
	}
	return nil
}

func setFlagFromCfg(flag *pflag.Flag, cfgVal reflect.Value) error {
	switch cfgVal.Kind() {
	case reflect.String:
		return flag.Value.Set(cfgVal.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if cfgVal.Type() == reflect.TypeOf(time.Duration(0)) {
			return flag.Value.Set(time.Duration(cfgVal.Int()).String())
		}
		return flag.Value.Set(fmt.Sprintf("%d", cfgVal.Int()))
	default:
		return fmt.Errorf("unsupported field kind %s", cfgVal.Kind())
	}
}
