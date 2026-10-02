/*
Copyright The Kubernetes Authors.

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

package filters

import (
	"errors"
	"fmt"
)

const (
	FilterScope                = "Scope"
	FilterClusterQueueSelector = "ClusterQueueSelector"
	FilterLabelSelector        = "LabelSelector"
	FilterPriority             = "Priority"
	FilterNumericLabels        = "NumericLabels"
	FilterPreemptorSelector    = "PreemptorSelector"

	ReasonUnsupportedScope        = "UnsupportedScope"
	ReasonInvalidSelector         = "InvalidSelector"
	ReasonUnsupportedMode         = "UnsupportedMode"
	ReasonUnsupportedComparison   = "UnsupportedComparison"
	ReasonMissingModeOrComparison = "MissingModeOrComparison"
)

// FilterBuildError describes a failure while building a candidate filter.
type FilterBuildError struct {
	Filter string
	Reason string
	Err    error
}

func (e *FilterBuildError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("building %s filter (%s): %v", e.Filter, e.Reason, e.Err)
	}
	return fmt.Sprintf("building %s filter (%s)", e.Filter, e.Reason)
}

func (e *FilterBuildError) Unwrap() error {
	return e.Err
}

// Is permits semantic matching with errors.Is.
func (e *FilterBuildError) Is(target error) bool {
	other, ok := target.(*FilterBuildError)
	if !ok || e == nil || other == nil {
		return e == other
	}
	return other.Filter == e.Filter &&
		other.Reason == e.Reason &&
		(other.Err == nil || errors.Is(e.Err, other.Err))
}
