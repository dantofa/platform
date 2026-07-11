// Package digitalocean holds the DigitalOcean client adapters: the godo-backed
// cluster client (DO REST API) and the S3-backed Spaces client. SDK-specific
// error types are translated at this boundary into the errors below, so the
// core and command layers never import the SDKs' error types.
package digitalocean

import (
	"errors"
	"fmt"

	"github.com/aws/smithy-go"
	"github.com/digitalocean/godo"
)

// MissingCredentialsError is returned when no API token / Spaces keys were
// supplied via argument or environment.
type MissingCredentialsError struct{ Hint string }

func (e *MissingCredentialsError) Error() string {
	return "missing DigitalOcean credentials: " + e.Hint
}

// MissingCredentials builds a MissingCredentialsError with a user-facing hint.
func MissingCredentials(hint string) error {
	return &MissingCredentialsError{Hint: hint}
}

// APIError is a failed call to DigitalOcean (REST or Spaces/S3). It carries the
// provider's raw error payload so the CLI can surface it verbatim rather than a
// reworded summary.
type APIError struct {
	payload any
}

// NewAPIError wraps a provider error payload.
func NewAPIError(payload any) *APIError { return &APIError{payload: payload} }

// Payload returns the provider's raw error body (satisfies render's payloader).
func (e *APIError) Payload() any { return e.payload }

func (e *APIError) Error() string { return fmt.Sprintf("%v", e.payload) }

// apiError translates a provider SDK error (godo REST or smithy/S3) into an
// APIError carrying the provider's raw error body, preserving it for the CLI to
// surface verbatim.
func apiError(err error) error {
	var doErr *godo.ErrorResponse
	if errors.As(err, &doErr) {
		payload := map[string]string{"message": doErr.Message}
		if doErr.RequestID != "" {
			payload["request_id"] = doErr.RequestID
		}
		return NewAPIError(payload)
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		return NewAPIError(map[string]string{
			"code":    api.ErrorCode(),
			"message": api.ErrorMessage(),
		})
	}
	return NewAPIError(err.Error())
}
