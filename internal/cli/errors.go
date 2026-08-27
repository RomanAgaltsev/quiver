package cli

import "fmt"

// exitError carries a specific process exit code and the cause, up to Execute.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }
func (e *exitError) Code() int     { return e.code }

// configErr marks a user/config error so it maps to exit code 2.
func configErr(err error) error {
	if err == nil {
		return nil
	}
	return &exitError{code: 2, err: err}
}

// runErr marks a transport or assertion failure so it maps to exit code 1.
func runErr(err error) error {
	if err == nil {
		return nil
	}
	return &exitError{code: 1, err: err}
}

// exitCodeErr is a bare exit code with no message (assertions already reported).
func exitCodeErr(code int) error {
	return &exitError{code: code, err: fmt.Errorf("%d assertion(s) or request(s) failed", code)}
}
