// Package domain defines the types, interfaces, and errors shared by the
// rest of the application. It imports no other internal package.
package domain

import "errors"

// Error kinds classify failures so callers, such as the HTTP layer, can react
// to a kind of failure without knowing every specific error. Check them with
// errors.Is.
var (
	ErrValidation = errors.New("validation error")
	ErrAuth       = errors.New("auth error")
	ErrAPI        = errors.New("api error")
	ErrConfig     = errors.New("config error")
)

// kindError is a sentinel error that belongs to one of the error kinds, so
// errors.Is matches both the sentinel and its kind.
type kindError struct {
	kind error
	msg  string
}

func (e *kindError) Error() string { return e.msg }
func (e *kindError) Unwrap() error { return e.kind }

func newKindError(kind error, msg string) error {
	return &kindError{kind: kind, msg: msg}
}

// Sentinel errors for common failures.
var (
	ErrMissingPRData       = newKindError(ErrValidation, "pr data missing")
	ErrInvalidPayload      = newKindError(ErrValidation, "invalid webhook payload")
	ErrInvalidSignature    = newKindError(ErrAuth, "invalid webhook signature")
	ErrMissingSignature    = newKindError(ErrAuth, "webhook signature missing")
	ErrInvalidPattern      = newKindError(ErrValidation, "invalid regex pattern")
	ErrEmptyPattern        = newKindError(ErrValidation, "pattern cannot be empty")
	ErrUnknownAction       = newKindError(ErrValidation, "unknown scheduled action")
	ErrFeatureDisabled     = newKindError(ErrConfig, "feature not enabled")
	ErrGroupNotFound       = newKindError(ErrAPI, "okta group not found")
	ErrTeamNotFound        = newKindError(ErrAPI, "github team not found")
	ErrInstallationMissing = newKindError(ErrConfig, "github app is not installed on the organization")
)
