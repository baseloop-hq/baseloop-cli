package output

import (
	"encoding/json"
	"fmt"
	"io"
)

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

type Envelope struct {
	OK      bool        `json:"ok"`
	Data    any         `json:"data,omitempty"`
	Summary string      `json:"summary,omitempty"`
	Error   *ErrorBody  `json:"error,omitempty"`
	Meta    interface{} `json:"meta,omitempty"`
}

func Success(data any, summary string, meta any) Envelope {
	if meta == nil {
		meta = map[string]any{}
	}
	return Envelope{OK: true, Data: data, Summary: summary, Meta: meta}
}

func Failure(code, message, hint string, meta any) Envelope {
	if meta == nil {
		meta = map[string]any{}
	}
	return Envelope{OK: false, Error: &ErrorBody{Code: code, Message: message, Hint: hint}, Meta: meta}
}

func JSON(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func Agent(w io.Writer, env Envelope) error {
	if env.OK {
		return JSON(w, env.Data)
	}
	return JSON(w, env)
}

func Text(w io.Writer, env Envelope) error {
	if !env.OK {
		if env.Error == nil {
			_, err := fmt.Fprintln(w, "Error")
			return err
		}
		if env.Error.Hint != "" {
			_, err := fmt.Fprintf(w, "%s: %s\nHint: %s\n", env.Error.Code, env.Error.Message, env.Error.Hint)
			return err
		}
		_, err := fmt.Fprintf(w, "%s: %s\n", env.Error.Code, env.Error.Message)
		return err
	}
	if env.Summary != "" {
		_, err := fmt.Fprintln(w, env.Summary)
		return err
	}
	return JSON(w, env.Data)
}
