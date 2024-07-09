/*
Copyright 2023 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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
