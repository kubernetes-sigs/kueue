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
	return &notFoundErrorMatch{}
}

type notFoundErrorMatch struct {
}

func (matcher *notFoundErrorMatch) Match(actual interface{}) (success bool, err error) {
	if actual == nil {
		return false, nil
	}

	err, ok := actual.(error)
	if !ok {
		return false, fmt.Errorf("NotFoundError expects an error")
	}

	return err != nil && apierrors.IsNotFound(err), nil
}

func (matcher *notFoundErrorMatch) FailureMessage(actual interface{}) (message string) {
	return format.Message(actual, "to be a NotFound error")
}

func (matcher *notFoundErrorMatch) NegatedFailureMessage(actual interface{}) (message string) {
	return format.Message(actual, "not to be NotFound error")
}

func BeForbiddenError() types.GomegaMatcher {
	return &isForbiddenErrorMatch{}
}

type isForbiddenErrorMatch struct {
}

func (matcher *isForbiddenErrorMatch) Match(actual interface{}) (success bool, err error) {
	err, ok := actual.(error)
	if !ok {
		return false, fmt.Errorf("ForbiddenError expects an error")
	}

	return err != nil && apierrors.IsForbidden(err), nil
}

func (matcher *isForbiddenErrorMatch) FailureMessage(actual interface{}) (message string) {
	return format.Message(actual, "to be a Forbidden error")
}

func (matcher *isForbiddenErrorMatch) NegatedFailureMessage(actual interface{}) (message string) {
	return format.Message(actual, "not to be Forbidden error")
}

type errorMatcher int

const (
	NotFoundError errorMatcher = iota
	ForbiddenError
	InvalidError
)

func (em errorMatcher) String() string {
	return []string{"NotFoundError", "ForbiddenError", "InvalidError"}[em]
}

type apiError func(error) bool

func (em errorMatcher) isAPIError(err error) bool {
	return []apiError{apierrors.IsNotFound, apierrors.IsForbidden, apierrors.IsInvalid}[em](err)
}

type isErrorMatch struct {
	name errorMatcher
}

func BeAPIError(name errorMatcher) types.GomegaMatcher {
	return &isErrorMatch{
		name: name,
	}
}

func (matcher *isErrorMatch) Match(actual interface{}) (success bool, err error) {
	err, ok := actual.(error)
	if !ok {
		return false, fmt.Errorf("%s expects an error", matcher.name.String())
	}

	return err != nil && matcher.name.isAPIError(err), nil
}

func (matcher *isErrorMatch) FailureMessage(actual interface{}) (message string) {
	return format.Message(actual, "to be a %s", matcher.name.String())
}

func (matcher *isErrorMatch) NegatedFailureMessage(actual interface{}) (message string) {
	return format.Message(actual, "not to be %s", matcher.name.String())
}
