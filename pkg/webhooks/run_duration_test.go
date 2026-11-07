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

package webhooks

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/validation/field"

	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
)

func TestValidateRunDuration(t *testing.T) {
	cases := map[string]struct {
		value   *int32
		old     *int32
		enabled bool
		want    []field.ErrorType
	}{
		"unset with gate off":               {},
		"positive":                          {value: new(int32(60)), enabled: true},
		"zero":                              {value: new(int32(0)), enabled: true, want: []field.ErrorType{field.ErrorTypeInvalid}},
		"negative":                          {value: new(int32(-1)), enabled: true, want: []field.ErrorType{field.ErrorTypeInvalid}},
		"gate off rejects new value":        {value: new(int32(60)), want: []field.ErrorType{field.ErrorTypeForbidden}},
		"gate off preserves existing value": {value: new(int32(60)), old: new(int32(60))},
		"gate off permits removal":          {old: new(int32(60))},
		"gate off rejects change":           {value: new(int32(30)), old: new(int32(60)), want: []field.ErrorType{field.ErrorTypeForbidden}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.WorkloadRunDuration, tc.enabled)
			cq := utiltestingapi.MakeClusterQueue("cq").Obj()
			oldCQ := cq.DeepCopy()
			cq.Spec.MaximumRunDurationSeconds = tc.value
			oldCQ.Spec.MaximumRunDurationSeconds = tc.old
			wl := utiltestingapi.MakeWorkload("wl", "ns").Obj()
			oldWL := wl.DeepCopy()
			wl.Spec.MaximumRunDurationSeconds = tc.value
			oldWL.Spec.MaximumRunDurationSeconds = tc.old
			for kind, errs := range map[string]field.ErrorList{
				"ClusterQueue": ValidateClusterQueueUpdate(oldCQ, cq),
				"Workload":     ValidateWorkloadUpdate(wl, oldWL),
			} {
				if len(errs) != len(tc.want) {
					t.Fatalf("%s errors = %v, want %v", kind, errs, tc.want)
				}
				for i := range errs {
					if errs[i].Type != tc.want[i] || errs[i].Field != "spec.maximumRunDurationSeconds" {
						t.Errorf("%s error = %v, want %v on duration", kind, errs[i], tc.want[i])
					}
				}
			}
		})
	}
}

func TestValidateLiveRunDurationOverride(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.WorkloadRunDuration, true)
	now := time.Unix(1000, 0)
	old := utiltestingapi.MakeWorkload("wl", "ns").SimpleReserveQuota("cq", "default", now).
		AdmittedAt(true, now).RunDuration(new(int32(60)), now).Obj()
	updated := old.DeepCopy()
	updated.Spec.MaximumRunDurationSeconds = new(int32(120))
	if errs := ValidateWorkloadUpdate(updated, old); len(errs) != 0 {
		t.Fatalf("live extension rejected: %v", errs)
	}

	unsupported := old.DeepCopy()
	unsupported.Status.Admission.RunDurationPolicy = nil
	unsupported.Status.RunDuration = nil
	unsupportedUpdate := unsupported.DeepCopy()
	unsupportedUpdate.Spec.MaximumRunDurationSeconds = new(int32(120))
	if errs := ValidateWorkloadUpdate(unsupportedUpdate, unsupported); len(errs) == 0 {
		t.Fatal("a live override bypassed an unsupported reservation")
	}
	updated.Status.Admission.RunDurationPolicy.DefaultSeconds = new(int32(90))
	if errs := ValidateWorkloadUpdate(updated, old); len(errs) == 0 {
		t.Fatal("changing this admission's captured default was allowed")
	}
}

func TestValidateRunDurationDisabledPreservesUntimedModes(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.WorkloadRunDuration, false)
	features.SetFeatureGateDuringTest(t, features.ConcurrentAdmission, true)
	wl := utiltestingapi.MakeWorkload("wl", "ns").MaximumRunDurationSeconds(60).Obj()
	concurrentadmission.SetParentVariantLabel(wl)
	if errs := ValidateWorkloadUpdate(wl, wl.DeepCopy()); len(errs) != 0 {
		t.Fatalf("disabled duration feature rejected an unchanged stored limit: %v", errs)
	}
}
