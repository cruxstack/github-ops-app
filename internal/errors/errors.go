// Package errors defines sentinel errors and domain types for the
// application. uses cockroachdb/errors for automatic stack trace capture.
package errors

import "github.com/cockroachdb/errors"

// error domain markers enable error classification and monitoring by type
// rather than comparing specific sentinel errors.
type (
	validationError struct{}
	authError       struct{}
	apiError        struct{}
	configError     struct{}
)

func (validationError) Error() string { return "validation error" }
func (authError) Error() string       { return "auth error" }
func (apiError) Error() string        { return "api error" }
func (configError) Error() string     { return "config error" }

// domain type instances for error marking
var (
	ValidationError = validationError{}
	AuthError       = authError{}
	APIError        = apiError{}
	ConfigError     = configError{}
)

// sentinel errors for common failure cases
var (
	ErrMissingPRData       = errors.Mark(errors.New("pr data missing"), ValidationError)
	ErrInvalidSignature    = errors.Mark(errors.New("invalid webhook signature"), AuthError)
	ErrMissingSignature    = errors.Mark(errors.New("signature missing but secret configured"), AuthError)
	ErrUnexpectedSignature = errors.Mark(errors.New("signature provided but secret not configured"), AuthError)
	ErrTeamNotFound        = errors.Mark(errors.New("github team not found"), APIError)
	ErrGroupNotFound       = errors.Mark(errors.New("okta group not found"), APIError)
	ErrInvalidPattern      = errors.Mark(errors.New("invalid regex pattern"), ValidationError)
	ErrEmptyPattern        = errors.Mark(errors.New("pattern cannot be empty"), ValidationError)
	ErrClientNotInit       = errors.Mark(errors.New("client not initialized"), ConfigError)
	ErrInvalidEventType    = errors.Mark(errors.New("unknown event type"), ValidationError)
	ErrMissingOAuthCreds   = errors.Mark(errors.New("must provide either api token or oauth credentials"), ConfigError)
	ErrOAuthTokenExpired   = errors.Mark(errors.New("oauth token expired"), AuthError)
)
