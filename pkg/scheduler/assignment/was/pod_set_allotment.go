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

package was

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
)

// placementBreakdown represents the spread of Pods on Nodes grouped by PodSet.
type placementBreakdown map[kueue.PodSetReference]podsPerNode

// podsPerNode represents the set of nodes assigned to the PodSet's Pods,
// mapped to the number of the PodSet's Pods to be placed on each node.
type podsPerNode map[types.NodeName]int32

// failuresBreakdown maps PodSets to a list of errors and failures
// reported by Pods when trying to schedule.
type failuresBreakdown map[kueue.PodSetReference]*failureSummary

type failureSummary struct {
	errs    []error
	reasons []string
}

func (a placementBreakdown) countPodPlacement(psRef kueue.PodSetReference, node types.NodeName) {
	if a[psRef] == nil {
		a[psRef] = make(podsPerNode)
	}
	a[psRef][node]++
}

func (f failuresBreakdown) add(psRef kueue.PodSetReference, pod client.ObjectKey, err error, reasons []string) {
	if _, ok := f[psRef]; !ok {
		f[psRef] = &failureSummary{}
	}
	f[psRef].record(pod, err, reasons)
}

func (fl *failureSummary) record(pod client.ObjectKey, err error, reasons []string) {
	fl.errs = append(fl.errs, fmt.Errorf("pod: %s, error: %w", pod, err))
	for _, reason := range reasons {
		fl.reasons = append(fl.reasons, fmt.Sprintf("pod: %s, failure reason: %s", pod, reason))
	}
}

func (fl *failureSummary) asStatus() flavorassigner.Status {
	return *flavorassigner.NewErrorStatus(errors.Join(fl.errs...), fl.reasons)
}
