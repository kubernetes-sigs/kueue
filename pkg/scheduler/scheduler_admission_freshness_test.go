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
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestFirstPassAdmissionDoesNotRetryChangedWorkload(t *testing.T) {
	for _, mergePatch := range []bool{false, true} {
		t.Run(fmt.Sprintf("mergePatch=%t", mergePatch), func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, mergePatch)
			for name, update := range map[string]func(*kueue.Workload){
				"new UID": func(wl *kueue.Workload) {
					wl.UID = "replacement"
				},
				"new generation": func(wl *kueue.Workload) {
					wl.Generation++
				},
				"allowed flavors changed": func(wl *kueue.Workload) {
					wl.Annotations = map[string]string{controllerconstants.WorkloadAllowedResourceFlavorAnnotation: "other"}
				},
				"new reservation": func(wl *kueue.Workload) {
					apimeta.SetStatusCondition(&wl.Status.Conditions, metav1.Condition{Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionTrue})
				},
				"admission check retry": func(wl *kueue.Workload) {
					wl.Status.AdmissionChecks = []kueue.AdmissionCheckState{{Name: "check", State: kueue.CheckStateRetry}}
				},
				"admission check rejected": func(wl *kueue.Workload) {
					wl.Status.AdmissionChecks = []kueue.AdmissionCheckState{{Name: "check", State: kueue.CheckStateRejected}}
				},
				"requeue blocked": func(wl *kueue.Workload) {
					apimeta.SetStatusCondition(&wl.Status.Conditions, metav1.Condition{Type: kueue.WorkloadRequeued, Status: metav1.ConditionFalse})
				},
				"in backoff": func(wl *kueue.Workload) {
					wl.Status.RequeueState = &kueue.RequeueState{RequeueAt: new(metav1.NewTime(time.Now().Add(time.Minute)))}
				},
			} {
				t.Run(name, func(t *testing.T) {
					wl := utiltestingapi.MakeWorkload("wl", "ns").UID("original").Generation(1).ResourceVersion("1").Obj()
					latest := wl.DeepCopy()
					latest.ResourceVersion = "2"
					update(latest)
					reader := utiltesting.NewClientBuilder().WithObjects(latest).Build()
					writes := 0
					fail := func() error {
						writes++
						return apierrors.NewConflict(kueue.Resource("workloads"), wl.Name, errors.New("new resource version"))
					}
					cl := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
						SubResourcePatch: func(_ context.Context, _ client.Client, _ string, _ client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
							return fail()
						},
						SubResourceApply: func(_ context.Context, _ client.Client, _ string, _ runtime.ApplyConfiguration, _ ...client.SubResourceApplyOption) error {
							return fail()
						},
					}).Build()
					s := &Scheduler{client: cl, admissionReader: reader, clock: clock.RealClock{}}
					admission := utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj()
					err := s.patchWorkloadAdmission(t.Context(), logr.Discard(), wl, &schdcache.ClusterQueueSnapshot{}, admission, nil)
					if !errors.Is(err, errAdmissionSnapshotChanged) {
						t.Errorf("patchWorkloadAdmission() error = %v, want %v", err, errAdmissionSnapshotChanged)
					}
					if writes != 1 {
						t.Errorf("admission writes = %d, want 1", writes)
					}
				})
			}
		})
	}
}
