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

func BeInvalidError() types.GomegaMatcher {
	return &isInvalidErrorMatch{}
}

type isInvalidErrorMatch struct {
}

func (matcher *isInvalidErrorMatch) Match(actual interface{}) (success bool, err error) {
	err, ok := actual.(error)
	if !ok {
		return false, fmt.Errorf("InvalidError expects an error")
	}

	return err != nil && apierrors.IsInvalid(err), nil
}

func (matcher *isInvalidErrorMatch) FailureMessage(actual interface{}) (message string) {
	return format.Message(actual, "to be a Invalid error")
}

func (matcher *isInvalidErrorMatch) NegatedFailureMessage(actual interface{}) (message string) {
	return format.Message(actual, "not to be Invalid error")
}
