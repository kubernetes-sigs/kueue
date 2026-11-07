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

package scheduler

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	testingclock "k8s.io/utils/clock/testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestRunDurationAdmissionPolicy(t *testing.T) {
	cases := map[string]struct {
		defaultSeconds  *int32
		override        *int32
		unsupported     bool
		multiKueue      bool
		concurrent      bool
		variant         bool
		queueConcurrent bool
		slicing         bool
		gateOff         bool
		wantError       bool
		wantPolicy      bool
	}{
		"default":  {defaultSeconds: new(int32(60)), wantPolicy: true},
		"override": {override: new(int32(90)), wantPolicy: true},
		"supported unlimited captures empty policy": {wantPolicy: true},
		"unsupported default":                       {defaultSeconds: new(int32(60)), unsupported: true, wantError: true},
		"unsupported override":                      {override: new(int32(90)), unsupported: true, wantError: true},
		"unsupported untimed remains eligible":      {unsupported: true},
		"multikueue default":                        {defaultSeconds: new(int32(60)), multiKueue: true, wantError: true},
		"multikueue override":                       {override: new(int32(90)), multiKueue: true, wantError: true},
		"multikueue untimed remains eligible":       {multiKueue: true},
		"concurrent default":                        {defaultSeconds: new(int32(60)), concurrent: true, wantError: true},
		"concurrent override":                       {override: new(int32(90)), concurrent: true, wantError: true},
		"concurrent untimed remains eligible":       {concurrent: true},
		"variant default":                           {defaultSeconds: new(int32(60)), variant: true, wantError: true},
		"variant override":                          {override: new(int32(90)), variant: true, wantError: true},
		"variant untimed remains eligible":          {variant: true},
		"concurrent queue default":                  {defaultSeconds: new(int32(60)), queueConcurrent: true, wantError: true},
		"concurrent queue override":                 {override: new(int32(90)), queueConcurrent: true, wantError: true},
		"concurrent queue untimed remains eligible": {queueConcurrent: true},
		"slicing default":                           {defaultSeconds: new(int32(60)), slicing: true, wantError: true},
		"slicing override":                          {override: new(int32(90)), slicing: true, wantError: true},
		"slicing untimed remains eligible":          {slicing: true},
		"gate off does not enable duration":         {defaultSeconds: new(int32(60)), gateOff: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.WorkloadRunDuration, !tc.gateOff)
			features.SetFeatureGateDuringTest(t, features.ConcurrentAdmission, true)
			ctx, log := utiltesting.ContextWithLog(t)
			c := utiltesting.NewFakeClient()
			cache := schdcache.New(c)
			cq := utiltestingapi.MakeClusterQueue("cq").Obj()
			cq.Spec.MaximumRunDurationSeconds = tc.defaultSeconds
			if tc.queueConcurrent {
				cq.Spec.ConcurrentAdmissionPolicy = &kueue.ConcurrentAdmissionPolicy{}
			}
			if tc.multiKueue {
				check := utiltestingapi.MakeAdmissionCheck("remote").ControllerName(kueue.MultiKueueControllerName).
					Active(metav1.ConditionTrue).Obj()
				cache.AddOrUpdateAdmissionCheck(log, check)
				cq.Spec.AdmissionChecksStrategy = &kueue.AdmissionChecksStrategy{
					AdmissionChecks: []kueue.AdmissionCheckStrategyRule{{Name: "remote"}},
				}
			}
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatal(err)
			}
			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			queue := snapshot.ClusterQueues()["cq"]
			wl := utiltestingapi.MakeWorkload("wl", "ns").Obj()
			wl.Spec.MaximumRunDurationSeconds = tc.override
			if tc.slicing {
				wl.Annotations = map[string]string{workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue}
			}
			if tc.concurrent {
				concurrentadmission.SetParentVariantLabel(wl)
			}
			if tc.variant {
				wl.OwnerReferences = []metav1.OwnerReference{{
					APIVersion: kueue.SchemeGroupVersion.String(), Kind: "Workload", Name: "parent", UID: "uid",
				}}
			}
			s := &Scheduler{
				clock:                testingclock.NewFakeClock(time.Unix(1000, 0)),
				runDurationSupported: func(*kueue.Workload) bool { return !tc.unsupported },
			}
			if message := s.runDurationAdmissionError(wl, queue); (message != "") != tc.wantError {
				t.Fatalf("admission error = %q, want error %v", message, tc.wantError)
			}
			if tc.wantError {
				return
			}
			admission := &kueue.Admission{ClusterQueue: "cq"}
			s.prepareWorkload(log, wl, queue, admission)
			if (wl.Status.Admission.RunDurationPolicy != nil) != tc.wantPolicy {
				t.Fatalf("captured policy = %v, want policy %v", wl.Status.Admission.RunDurationPolicy, tc.wantPolicy)
			}
			if !tc.wantPolicy {
				queue.MaximumRunDurationSeconds = new(int32(120))
				if message := s.runDurationAdmissionError(wl, queue); message != "" {
					t.Fatalf("a new queue default affected an existing untimed reservation: %s", message)
				}
				return
			}
			first := wl.Status.Admission.RunDurationPolicy.DeepCopy()
			queue.MaximumRunDurationSeconds = new(int32(120))
			s.prepareWorkload(log, wl, queue, &kueue.Admission{ClusterQueue: "cq"})
			got := wl.Status.Admission.RunDurationPolicy.DefaultSeconds
			if first.DefaultSeconds == nil && got != nil || first.DefaultSeconds != nil && (got == nil || *got != *first.DefaultSeconds) {
				t.Fatalf("second pass changed captured default from %v to %v", first.DefaultSeconds, got)
			}
		})
	}
}
