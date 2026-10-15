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

// podsPerNode represents the count of Pods assignet to each recorded Node.
type podsPerNode map[types.NodeName]int32

// failuresBreakdown maps PodSets to a list of errors and failures
// reported by Pods when trying to schedule.
type failuresBreakdown map[kueue.PodSetReference]*failureSummary

type failureSummary struct {
	errs    []error
	reasons []string
}

func (pb placementBreakdown) countPodPlacement(psRef kueue.PodSetReference, node types.NodeName) {
	if pb[psRef] == nil {
		pb[psRef] = make(podsPerNode)
	}
	pb[psRef][node]++
}

func (fb failuresBreakdown) add(psRef kueue.PodSetReference, pod client.ObjectKey, err error, reasons []string) {
	if _, ok := fb[psRef]; !ok {
		fb[psRef] = &failureSummary{}
	}
	fb[psRef].record(pod, err, reasons)
}

func (fs *failureSummary) record(pod client.ObjectKey, err error, reasons []string) {
	if err != nil {
		fs.errs = append(fs.errs, fmt.Errorf("pod: %s, error: %w", pod, err))
	}
	for _, reason := range reasons {
		fs.reasons = append(fs.reasons, fmt.Sprintf("pod: %s, failure reason: %s", pod, reason))
	}
}

func (fs *failureSummary) asStatus() *flavorassigner.Status {
	return flavorassigner.NewStatus(fs.reasons...).WithError(errors.Join(fs.errs...))
}
