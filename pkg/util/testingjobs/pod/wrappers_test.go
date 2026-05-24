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

package pod

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestPreemptionPolicy(t *testing.T) {
	p := MakePod("pod", "ns").PreemptionPolicy(corev1.PreemptNever).Obj()

	if p.Spec.PreemptionPolicy == nil {
		t.Fatalf("preemptionPolicy = nil, want %q", corev1.PreemptNever)
	}
	if got := *p.Spec.PreemptionPolicy; got != corev1.PreemptNever {
		t.Fatalf("preemptionPolicy = %q, want %q", got, corev1.PreemptNever)
	}
}
