package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/nxssie/nan-cli/internal/build"
)

// Error is a refusal from the dashboard's half of the platform.
//
// The platform answers with `{"error": "..."}` in words already meant for a
// person, so the message is passed on as it came; only an answer without one
// is described by its status.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	switch e.Status {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return fmt.Sprintf("nan.builders is having trouble (HTTP %d) - try again in a moment", e.Status)
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// maxMessageLength keeps a hostile or broken server from filling the screen
// with whatever it put in the error field.
const maxMessageLength = 200

func parseError(status int, body []byte) error {
	var wire struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &wire); err == nil && wire.Error != "" {
		msg := wire.Error
		if len(msg) > maxMessageLength {
			msg = msg[:maxMessageLength] + "…"
		}
		return &Error{Status: status, Message: msg}
	}
	return &Error{Status: status}
}

// userAgent says which CLI, and which version of it, is asking.
func userAgent() string {
	return build.Name + "-cli/" + build.Version
}
