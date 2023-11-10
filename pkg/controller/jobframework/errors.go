package jobframework

import "errors"

// UnretryableError is an error that doesn't require reconcile retry
// and will not be returned by the JobReconciler.
func UnretryableError(msg string) error {
	return &unretryableError{msg: msg}
}

type unretryableError struct {
	msg string
}

func (e *unretryableError) Error() string {
	return e.msg
}

func IsUnretryableError(e error) bool {
	var unretryableError *unretryableError
	return errors.As(e, &unretryableError)
}
