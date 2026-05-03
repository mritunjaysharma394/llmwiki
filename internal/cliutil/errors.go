// Package cliutil holds CLI-only helpers that don't belong in domain packages.
// UserError is the structured error type rendered by cmd/root.go's Execute().
package cliutil

import "fmt"

// UserError is an operator-facing error: a one-line cause, an optional wrapped
// underlying error (to print indented under "cause:"), and a one-line
// remediation hint. The Render function formats the three lines; Error()
// flattens them so non-Render code paths still produce a useful string.
type UserError struct {
	Cause       string // one line, no trailing period
	Wrapped     error  // optional underlying error
	Remediation string // imperative, e.g. "re-run with --max-retries=3"
}

func (e *UserError) Error() string {
	if e.Wrapped != nil {
		return fmt.Sprintf("%s: %v (try: %s)", e.Cause, e.Wrapped, e.Remediation)
	}
	return fmt.Sprintf("%s (try: %s)", e.Cause, e.Remediation)
}

func (e *UserError) Unwrap() error { return e.Wrapped }

// Wrap is a small constructor used by command-package callers.
func Wrap(cause string, wrapped error, remediation string) *UserError {
	return &UserError{Cause: cause, Wrapped: wrapped, Remediation: remediation}
}

// Render formats any error for display by Execute(). UserError gets a 3-line
// block; everything else gets the one-line "Error: <err>" treatment.
func Render(err error) string {
	if err == nil {
		return ""
	}
	var ue *UserError
	if asUserError(err, &ue) {
		if ue.Wrapped != nil {
			return fmt.Sprintf("Error: %s\n  cause: %v\n  try:   %s", ue.Cause, ue.Wrapped, ue.Remediation)
		}
		return fmt.Sprintf("Error: %s\n  try:   %s", ue.Cause, ue.Remediation)
	}
	return fmt.Sprintf("Error: %v", err)
}

// asUserError centralizes the errors.As call so callers don't need to import
// the standard "errors" package alongside cliutil.
func asUserError(err error, target **UserError) bool {
	for cur := err; cur != nil; {
		if ue, ok := cur.(*UserError); ok {
			*target = ue
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := cur.(unwrapper)
		if !ok {
			return false
		}
		cur = u.Unwrap()
	}
	return false
}
