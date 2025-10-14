package git

import (
	"errors"
	"fmt"
)

// ErrGitOp represents an error that occurred during a git operation.
type ErrGitOp struct {
	Op     string // The git operation that failed (e.g., the git command).
	Err    error  // The underlying error that caused the failure.
	Status int    // The exit status code of the git command.
}

// NewErrGitOp creates a new ErrGitOp instance.
func NewErrGitOp(op string, err error, status int) *ErrGitOp {
	return &ErrGitOp{Op: op, Err: err, Status: status}
}

// Error returns a formatted error message for the ErrGitOp.
func (e *ErrGitOp) Error() string {
	return fmt.Sprintf("command '%s' failed with %s", e.Op, e.Err.Error())
}

// Unwrap returns the underlying error.
func (e *ErrGitOp) Unwrap() error { return e.Err }

// IsConflictErr checks if the error is due to a git conflict (exit status 1).
func IsConflictErr(err error) bool {
	var gitErr *ErrGitOp
	return errors.As(err, &gitErr) && gitErr.Status == 1
}
