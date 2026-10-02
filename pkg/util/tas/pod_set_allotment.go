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

package tas

import (
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// Allotments represents the spread of PodSets accross alloted nodes,
// with the breakdown of each PodSet's Pod placements by Node.
type Allotments map[kueue.PodSetReference]Allotment

// Allotment represents the set of nodes assignet to the PodSet's Pods,
// mapped to the number of the PodSet's Pods to be placed on each note.
type Allotment map[NodeName]int32

type AllotmentFailures map[kueue.PodSetReference]failuresList

type failuresList []allotmentFailure

type allotmentFailure struct {
	Pod     client.ObjectKey
	Error   error
	Reasons []string
}

func (a Allotments) RecordAllotment(psRef kueue.PodSetReference, node NodeName) {
	if a[psRef] == nil {
		a[psRef] = make(Allotment)
	}
	a[psRef][node]++
}

func (f AllotmentFailures) RecordFailure(psRef kueue.PodSetReference, pod client.ObjectKey, reasons []string) {
	f[psRef] = append(f[psRef], allotmentFailure{Pod: pod, Reasons: reasons})
}

func (f AllotmentFailures) RecordError(psRef kueue.PodSetReference, pod client.ObjectKey, err error, reasons []string) {
	f[psRef] = append(f[psRef], allotmentFailure{Pod: pod, Error: err, Reasons: reasons})
}

func (fl failuresList) Summarize() ([]string, error) {
	var errs []error
	var reasons []string
	for _, failure := range fl {
		if failure.Error != nil {
			errs = append(errs, fmt.Errorf("pod: %s, error: %w", failure.Pod, failure.Error))
		}
		if len(failure.Reasons) > 0 {
			for _, reason := range failure.Reasons {
				reasons = append(reasons, fmt.Sprintf("pod: %s, failure reason: %s", failure.Pod, reason))
			}
		}
	}
	return reasons, errors.Join(errs...)
}
