package main

import (
	"encoding/json"
	"errors"
	"os"
	"syscall"

	"github.com/3kh0/subswapper-fx/internal/subswapper"
)

// launchDiagnostic contains only fixed, safe text. Delegated home runs may
// expose these diagnostics, but must continue hiding arbitrary underlying errors.
type launchDiagnostic string

func (e launchDiagnostic) Error() string { return string(e) }

const (
	launchReadOnly           launchDiagnostic = "launcher filesystem is read-only; allow writes to Subswapper state and runtime storage, then retry"
	launchPermission         launchDiagnostic = "launcher filesystem access was denied; check Subswapper state and runtime storage permissions, then retry"
	launchStateInvalid       launchDiagnostic = "launcher state is invalid; repair or restore Subswapper state before retrying"
	launchTokenMissing       launchDiagnostic = "Claude setup token is missing; store one with `subswapper home token set`"
	launchTokenExpired       launchDiagnostic = "Claude setup token has expired; replace it with `subswapper home token set`"
	launchTokenMismatch      launchDiagnostic = "Claude setup token metadata does not match secure storage; replace it with `subswapper home token set`"
	launchTokenInvalid       launchDiagnostic = "Claude setup token storage is invalid; check storage permissions and replace it with `subswapper home token set`"
	launchTokenUnavailable   launchDiagnostic = "Claude setup token could not be loaded; check Subswapper state and secure storage"
	launchRuntimeUnavailable launchDiagnostic = "Claude runtime home could not be prepared; check runtime storage"
	launchExecutable         launchDiagnostic = "Claude executable could not be started; check its installation and execution permissions"
	launchAuthFailed         launchDiagnostic = "Claude authentication check failed; check provider availability and authentication before retrying"
	launchAuthTimeout        launchDiagnostic = "Claude authentication check timed out; check provider availability before retrying"
	launchAuthUnusable       launchDiagnostic = "Claude authentication is not usable; check the selected account setup token and provider authentication"
)

// classifyLauncherFailure preserves unknown errors for callers that already
// own their fallback, and never incorporates path or JSON payload text.
func classifyLauncherFailure(err error) error {
	var syntax *json.SyntaxError
	var mismatch *json.UnmarshalTypeError
	switch {
	case errors.Is(err, syscall.EROFS):
		return launchReadOnly
	case errors.Is(err, os.ErrPermission):
		return launchPermission
	case errors.As(err, &syntax), errors.As(err, &mismatch):
		return launchStateInvalid
	default:
		return err
	}
}

func diagnoseClaudeTokenFailure(err error) error {
	var diagnostic launchDiagnostic
	if classified := classifyLauncherFailure(err); errors.As(classified, &diagnostic) {
		return diagnostic
	}
	switch {
	case errors.Is(err, subswapper.ErrClaudeSetupTokenNotConfigured):
		return launchTokenMissing
	case errors.Is(err, subswapper.ErrClaudeSetupTokenExpired):
		return launchTokenExpired
	case errors.Is(err, subswapper.ErrClaudeSetupTokenRevisionMismatch):
		return launchTokenMismatch
	case errors.Is(err, subswapper.ErrClaudeSetupTokenStorageUnsafe), errors.Is(err, subswapper.ErrClaudeSetupTokenMalformed):
		return launchTokenInvalid
	default:
		return launchTokenUnavailable
	}
}
