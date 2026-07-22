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

package flavorassigner

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// Sentinel errors for TAS request building, so callers/tests can identify the
// failure with errors.Is instead of matching on the message text.
var (
	ErrElasticRequiredTopologyNotSupported  = errors.New("required topology is not supported with ElasticJobsViaWorkloadSlices")
	ErrElasticPreferredTopologyNotSupported = errors.New("preferred topology is not supported with ElasticJobsViaWorkloadSlices")
	ErrNoTASCacheInformation                = errors.New("workload requires Topology, but there is no TAS cache information")
	ErrNoTASFlavorAssigned                  = errors.New("no TAS flavor assigned")
)

// MultipleTASFlavorsAssignedError indicates that a PodSet ended up with more
// than one TAS flavor assigned across its resources, which TAS cannot handle.
// The offending flavors are kept as a field rather than only in the message so
// callers can inspect them with errors.As instead of parsing the error text.
type MultipleTASFlavorsAssignedError struct {
	Flavors []kueue.ResourceFlavorReference
}

func (e *MultipleTASFlavorsAssignedError) Error() string {
	names := make([]string, len(e.Flavors))
	for i, f := range e.Flavors {
		names[i] = string(f)
	}
	return fmt.Sprintf("more than one TAS flavor assigned: %s", strings.Join(names, ", "))
}

// Is reports equality for MultipleTASFlavorsAssignedError based on the
// offending flavor set, so callers can use errors.Is for semantic matching.
func (e *MultipleTASFlavorsAssignedError) Is(target error) bool {
	other, ok := target.(*MultipleTASFlavorsAssignedError)
	if !ok {
		return false
	}
	if e == nil || other == nil {
		return e == other
	}

	eFlavors := append([]kueue.ResourceFlavorReference(nil), e.Flavors...)
	otherFlavors := append([]kueue.ResourceFlavorReference(nil), other.Flavors...)
	slices.Sort(eFlavors)
	slices.Sort(otherFlavors)

	return slices.Equal(eFlavors, otherFlavors)
}
