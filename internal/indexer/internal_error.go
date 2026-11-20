package indexer

import "errors"

// InternalError marks a failure of indexarr itself — a storage error, a
// configuration that cannot be decoded, a bug in the result handling — as
// opposed to an answer from the site. The health model, the auto-disable
// policy and the notifications read the distinction: only a remote failure may
// degrade a source.
type InternalError struct {
	Op  string
	Err error
}

func (e *InternalError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return e.Op
	}
	if e.Op == "" {
		return e.Err.Error()
	}
	return e.Op + ": " + e.Err.Error()
}

func (e *InternalError) Unwrap() error { return e.Err }

// Internal marks err as an indexarr failure with the operation that failed.
// It is idempotent: an already marked error is returned unchanged, so a
// caller can wrap the same chain twice without losing the first operation.
func Internal(op string, err error) error {
	if err == nil {
		return nil
	}
	var existing *InternalError
	if errors.As(err, &existing) {
		return err
	}
	return &InternalError{Op: op, Err: err}
}

// IsInternal reports whether err was caused by indexarr rather than by the
// site. The check walks the whole chain.
func IsInternal(err error) bool {
	var target *InternalError
	return errors.As(err, &target)
}
