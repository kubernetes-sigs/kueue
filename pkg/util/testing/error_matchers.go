/*
Copyright 2022 The Kubernetes Authors.

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

package testing

import (
	"fmt"

	"github.com/onsi/gomega/format"
	"github.com/onsi/gomega/types"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func BeNotFoundError() types.GomegaMatcher {
	return BeError(NotFoundError)
}

func BeForbiddenError() types.GomegaMatcher {
	return BeError(ForbiddenError)
}

func BeInvalidError() types.GomegaMatcher {
	return BeError(InvalidError)
}

type ErrorType int

const (
	NotFoundError ErrorType = iota
	ForbiddenError
	InvalidError
)

func (em ErrorType) String() string {
	return []string{"NotFoundError", "ForbiddenError", "InvalidError"}[em]
}

type isError func(error) bool

func (em ErrorType) IsError(err error) bool {
	return []isError{apierrors.IsNotFound, apierrors.IsForbidden, apierrors.IsInvalid}[em](err)
}

type errorMatcher struct {
	errorType ErrorType
}

func BeError(errorType ErrorType) types.GomegaMatcher {
	return &errorMatcher{
		errorType: errorType,
	}
}

func (matcher *errorMatcher) Match(actual interface{}) (success bool, err error) {
	err, ok := actual.(error)
	if !ok {
		return false, fmt.Errorf("Error matcher expects an error.  Got:\n%s", format.Object(actual, 1))
	}

	return err != nil && matcher.errorType.IsError(err), nil
}

func (matcher *errorMatcher) FailureMessage(actual interface{}) (message string) {
	return fmt.Sprintf("Expected %s, but got:\n%s", matcher.errorType.String(), format.Object(actual, 1))
}

func (matcher *errorMatcher) NegatedFailureMessage(interface{}) (message string) {
	return fmt.Sprintf("Expected not to be %s", matcher.errorType.String())
}
