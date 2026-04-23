package cmd

import (
	"encoding/json"
	"io"
)

type jsonEnvelope struct {
	SchemaVersion int `json:"schema_version"`
	Data          any `json:"data"`
}

func writeJSON(w io.Writer, v any) error {
	env := jsonEnvelope{SchemaVersion: 1, Data: v}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}
