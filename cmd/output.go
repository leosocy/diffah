package cmd

import (
	"encoding/json"
	"io"
)

type jsonEnvelope struct {
	SchemaVersion int `json:"schema_version"`
	Data          any `json:"data"`
}

type jsonErrorEnvelope struct {
	SchemaVersion int        `json:"schema_version"`
	Error         errPayload `json:"error"`
}

type errPayload struct {
	Category   string `json:"category"`
	Message    string `json:"message"`
	NextAction string `json:"next_action,omitempty"`
}

func writeJSON(w io.Writer, v any) error {
	env := jsonEnvelope{SchemaVersion: 1, Data: v}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

func writeJSONError(w io.Writer, cat, msg, hint string) error {
	env := jsonErrorEnvelope{
		SchemaVersion: 1,
		Error: errPayload{
			Category:   cat,
			Message:    msg,
			NextAction: hint,
		},
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}
