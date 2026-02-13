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

package multikueue

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workloadslicing"

	_ "sigs.k8s.io/kueue/pkg/controller/jobs"
)

var errFake = errors.New("fake error")

func TestWlReconcile(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)

	objCheckOpts := cmp.Options{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
		cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime"),
		cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
	}

	baseWorkloadBuilder := utiltestingapi.MakeWorkload("wl1", TestNamespace)
	baseJobBuilder := testingjob.MakeJob("job1", TestNamespace).Suspend(false)
	baseJobManagedByKueueBuilder := baseJobBuilder.Clone().ManagedBy(kueue.MultiKueueControllerName)

	cases := map[string]struct {
		features map[featuregate.Feature]bool

		reconcileFor             string
		managersWorkloads        []kueue.Workload
		managersJobs             []batchv1.Job
		managersDeletedWorkloads []*kueue.Workload
		worker1Workloads         []kueue.Workload
		worker1Jobs              []batchv1.Job
		dispatcherName           *string

		// second worker
		useSecondWorker      bool
		worker2Reconnecting  bool
		worker2OnDeleteError error
		worker2OnGetError    error
		worker2OnCreateError error
		worker2Workloads     []kueue.Workload
		worker2Jobs          []batchv1.Job

		wantError             error
		wantEvents            []utiltesting.EventRecord
		wantManagersWorkloads []kueue.Workload
		wantManagersJobs      []batchv1.Job
		wantWorker1Workloads  []kueue.Workload
		wantWorker1Jobs       []batchv1.Job

		// second worker
		wantWorker2Workloads []kueue.Workload
		wantWorker2Jobs      []batchv1.Job
	}{
		"deleted regular workload is removed from the cache": {
			reconcileFor: "wl1",
			managersJobs: []batchv1.Job{*baseJobBuilder.Clone().Obj()},
			managersDeletedWorkloads: []*kueue.Workload{
				baseWorkloadBuilder.Clone().
					DeletionTimestamp(now).
					Finalizers(kueue.ResourceInUseFinalizerName).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{*baseJobBuilder.Clone().Obj()},
		},
		"deleted MultiKueue workload is deleted from cache - the worker will be deleted by GC": {
			reconcileFor: "wl1",
			managersJobs: []batchv1.Job{*baseJobManagedByKueueBuilder.Clone().Obj()},
			managersDeletedWorkloads: []*kueue.Workload{
				baseWorkloadBuilder.Clone().
					DeletionTimestamp(now).
					Finalizers(kueue.ResourceInUseFinalizerName).
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStateRejected}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					Obj(),
			},
			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{*baseJobManagedByKueueBuilder.Clone().Obj()},
			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
		},
		"missing workload": {
			reconcileFor: "missing workload",
		},
		"missing workload (in deleted workload cache)": {
			reconcileFor: "wl1",
			managersDeletedWorkloads: []*kueue.Workload{
				baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
			worker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
		},
		"missing workload (in deleted workload cache), no remote objects": {
			reconcileFor: "wl1",
			managersDeletedWorkloads: []*kueue.Workload{
				baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
		},
		"unmanaged wl (no ac) is ignored": {
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().Obj(),
			},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().Obj(),
			},
		},
		"unmanaged wl (no parent) is rejected": {
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					Obj(),
			},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStateRejected, Message: "No multikueue adapter found"}).
					Obj(),
			},
		},
		"unmanaged wl (owned by pod) is rejected": {
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "uid1").
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					Obj(),
			},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "uid1").
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStateRejected, Message: `No multikueue adapter found for owner kind "/v1, Kind=Pod"`}).
					Obj(),
			},
		},
		"unmanaged wl (job not managed by multikueue) is rejected": {
			reconcileFor: "wl1",
			managersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().Obj(),
			},
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().Obj(),
			},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStateRejected, Message: `The owner is not managed by Kueue: Expecting spec.managedBy to be "kueue.x-k8s.io/multikueue" not ""`}).
					Obj(),
			},
		},
		"failing to read from a worker": {
			reconcileFor: "wl1",
			managersJobs: []batchv1.Job{*baseJobManagedByKueueBuilder.Clone().Obj()},
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					Obj(),
			},
			useSecondWorker:   true,
			worker2OnGetError: errFake,

			wantManagersJobs: []batchv1.Job{*baseJobManagedByKueueBuilder.Clone().Obj()},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					Obj(),
			},
			wantError: errFake,
		},
		"reconnecting clients are skipped": {
			reconcileFor: "wl1",
			managersJobs: []batchv1.Job{*baseJobManagedByKueueBuilder.Clone().Obj()},
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					Obj(),
			},
			useSecondWorker:     true,
			worker2Reconnecting: true,
			worker2OnGetError:   errFake,

			wantManagersJobs: []batchv1.Job{*baseJobManagedByKueueBuilder.Clone().Obj()},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					Obj(),
			},
			wantError: nil,
		},
		"wl without reservation, clears the workload objects": {
			reconcileFor: "wl1",
			managersJobs: []batchv1.Job{*baseJobManagedByKueueBuilder.Clone().Obj()},
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					Obj(),
			},
			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{*baseJobManagedByKueueBuilder.Clone().Obj()},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					Obj(),
			},
		},
		"wl with reservation, creates remote workloads, worker2 fails": {
			reconcileFor: "wl1",
			managersJobs: []batchv1.Job{*baseJobManagedByKueueBuilder.Clone().Obj()},
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
			useSecondWorker:      true,
			worker2OnCreateError: errFake,

			wantManagersJobs: []batchv1.Job{*baseJobManagedByKueueBuilder.Clone().Obj()},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					NominatedClusterNames("worker1", "worker2").
					Obj(),
			},
			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			wantError: errFake,
		},
		"wl with reservation, creates missing workloads": {
			reconcileFor: "wl1",
			managersJobs: []batchv1.Job{*baseJobManagedByKueueBuilder.Clone().Obj()},
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			useSecondWorker: true,

			wantManagersJobs: []batchv1.Job{*baseJobManagedByKueueBuilder.Clone().Obj()},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					NominatedClusterNames("worker1", "worker2").
					Obj(),
			},
			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},

			wantWorker2Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
		},
		"remote wl with reservation, unable to delete the second worker's workload": {
			features:     map[featuregate.Feature]bool{features.MultiKueueWaitForWorkloadAdmitted: false},
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},

			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},

			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
			useSecondWorker:      true,
			worker2OnDeleteError: errFake,
			worker2Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Obj(),
			},

			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},

			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
			wantWorker2Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Obj(),
			},
			wantError: errFake,
		},
		"remote wl with reservation": {
			features: map[featuregate.Feature]bool{
				features.MultiKueueWaitForWorkloadAdmitted: false,
			},
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},

			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},

			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			useSecondWorker: true,
			worker2Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Obj(),
			},

			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					ClusterName("worker1").
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},

			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
			wantWorker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},

			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkloadBuilder.Clone().Obj()),
					EventType: "Normal",
					Reason:    "MultiKueue",
					Message:   `The workload got reservation on "worker1"`,
				},
			},
		},
		"remote wl with reservation but not admitted, feature gate enabled - other workers not deleted": {
			features: map[featuregate.Feature]bool{
				features.MultiKueueWaitForWorkloadAdmitted: true,
			},
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			useSecondWorker: true,
			worker2Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					NominatedClusterNames("worker1", "worker2").
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
			wantWorker2Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
		},
		"remote wl admitted, feature gate enabled - other workers deleted": {
			features: map[featuregate.Feature]bool{
				features.MultiKueueWaitForWorkloadAdmitted: true,
			},
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadAdmitted,
						Status: metav1.ConditionTrue,
						Reason: "Admitted",
					}).
					Obj(),
			},
			useSecondWorker: true,
			worker2Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					ClusterName("worker1").
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadAdmitted,
						Status: metav1.ConditionTrue,
						Reason: "Admitted",
					}).
					Obj(),
			},
			wantWorker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkloadBuilder.Clone().Obj()),
					EventType: "Normal",
					Reason:    "MultiKueue",
					Message:   `The workload got reservation on "worker1"`,
				},
			},
		},
		"remote wl evicted due to eviction on manager cluster": {
			features: map[featuregate.Feature]bool{
				features.MultiKueueWaitForWorkloadAdmitted: false,
			},
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					ClusterName("worker1").
					EvictedAt(now).
					Obj(),
			},
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
			worker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			useSecondWorker: true,
			worker2Workloads: []kueue.Workload{
				*baseWorkloadBuilder.DeepCopy(),
			},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					ClusterName("worker1").
					EvictedAt(now).
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.DeepCopy(),
			},
			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadDeactivationTarget,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedOnManagerCluster,
						Message: "Evicted on manager: Evicted by test",
					}).
					Obj(),
			},
			wantWorker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
		},
		"handle workload evicted on manager cluster": {
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					ClusterName("worker1").
					EvictedAt(now).
					Obj(),
			},
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Active(1).Obj(),
			},
			worker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Active(0).
					Obj(),
			},
			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  "DeactivatedDueToEvictedOnManagerCluster",
						Message: "Evicted on manager: Evicted by test",
					}).
					Obj(),
			},
			useSecondWorker: true,
			worker2Workloads: []kueue.Workload{
				*baseWorkloadBuilder.DeepCopy(),
			},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					ClusterName("worker1").
					EvictedAt(now).
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Active(0).Obj(),
			},
			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  "DeactivatedDueToEvictedOnManagerCluster",
						Message: "Evicted on manager: Evicted by test",
					}).
					Obj(),
			},
			wantWorker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			wantWorker2Workloads: []kueue.Workload{
				*baseWorkloadBuilder.DeepCopy(),
			},
		},
		"handle workload evicted on worker cluster": {
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					ClusterName("worker1").
					Obj(),
			},
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Active(1).Obj(),
			},
			worker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Active(1).
					Obj(),
			},
			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  "DeactivatedDueToEvictedOnWorkerCluster",
						Message: "Evicted on worker: Evicted by test",
					}).
					EvictedAt(now).
					Obj(),
			},
			useSecondWorker: true,
			worker2Workloads: []kueue.Workload{
				*baseWorkloadBuilder.DeepCopy(),
			},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateRetry,
						Message: `Workload evicted on worker cluster: "worker1", resetting for re-admission. Previously: "Ready"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					ClusterName("worker1").
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Active(1).Obj(),
			},
			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  "ByTest",
						Message: "Evicted by test",
					}).
					Obj(),
			},
			wantWorker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Active(1).
					Obj(),
			},
			wantWorker2Workloads: []kueue.Workload{
				*baseWorkloadBuilder.DeepCopy(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkloadBuilder.Clone().Obj()),
					EventType: "Normal",
					Reason:    "MultiKueue",
					Message:   `Workload evicted on worker cluster: "worker1", resetting for re-admission. Previously: "Ready"`,
				},
			},
		},
		"remote job is changing status the local Job is updated ": {
			features: map[featuregate.Feature]bool{
				features.MultiKueueWaitForWorkloadAdmitted: false,
			},
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},

			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},

			worker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Active(1).
					Obj(),
			},

			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					ClusterName("worker1").
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().
					Active(1).
					Obj(),
			},

			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
			wantWorker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Active(1).
					Obj(),
			},

			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkloadBuilder.Clone().Obj()),
					EventType: "Normal",
					Reason:    "MultiKueue",
					Message:   `The workload got reservation on "worker1"`,
				},
			},
		},
		"remote wl is finished, the local workload and Job are marked completed ": {
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},

			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},

			worker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
					Obj(),
			},

			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "ByTest", Message: "by test"}).
					Obj(),
			},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "ByTest", Message: `by test`}).
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().
					Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
					Obj(),
			},

			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "ByTest", Message: "by test"}).
					Obj(),
			},
			wantWorker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
					Obj(),
			},
		},
		"the local Job is marked finished, the remote objects are removed": {
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStatePending,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "ByTest", Message: `by test`}).
					Obj(),
			},

			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().
					Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
					Obj(),
			},

			worker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
					Obj(),
			},

			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "ByTest", Message: "by test"}).
					Obj(),
			},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStatePending,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "ByTest", Message: `by test`}).
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().
					Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
					Obj(),
			},
		},
		"the local workload admission check Ready if the remote WorkerLostTimeout is not exceeded": {
			reconcileFor: "wl1",
			managersJobs: []batchv1.Job{*baseJobManagedByKueueBuilder.Clone().Obj()},
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:               "ac1",
						State:              kueue.CheckStateReady,
						LastTransitionTime: metav1.NewTime(now.Add(-defaultWorkerLostTimeout / 2)), // 50% of the timeout
						Message:            `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{*baseJobManagedByKueueBuilder.Clone().Obj()},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
		},
		"the local workload's admission check is set to Retry if the WorkerLostTimeout is exceeded": {
			reconcileFor: "wl1",
			managersJobs: []batchv1.Job{*baseJobManagedByKueueBuilder.Clone().Obj()},
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:               "ac1",
						State:              kueue.CheckStateReady,
						LastTransitionTime: metav1.NewTime(now.Add(-defaultWorkerLostTimeout * 3 / 2)), // 150% of the timeout
						Message:            `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{*baseJobManagedByKueueBuilder.Clone().Obj()},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateRetry,
						Message: `Reserving remote lost`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
		},
		"worker reconnects after the local workload is requeued, remote objects are deleted": {
			reconcileFor: "wl1",
			managersJobs: []batchv1.Job{*baseJobManagedByKueueBuilder.Clone().Obj()},
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStatePending,
						Message: `Requeued`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					Obj(),
			},

			wantManagersJobs: []batchv1.Job{*baseJobManagedByKueueBuilder.Clone().Obj()},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStatePending,
						Message: `Requeued`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					Obj(),
			},

			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
			worker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Obj(),
			},
		},
		"worker reconnects after the local workload is requeued and got reservation on a second worker": {
			features: map[featuregate.Feature]bool{
				features.MultiKueueWaitForWorkloadAdmitted: false,
			},
			// the worker with the oldest reservation is kept
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker2"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					QuotaReservedTime(now.Add(-time.Hour)). // one hour ago
					Obj(),
			},
			worker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Obj(),
			},
			useSecondWorker: true,
			worker2Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					QuotaReservedTime(now.Add(-time.Minute)). // one minute ago
					Obj(),
			},
			worker2Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Obj(),
			},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					ClusterName("worker1").
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
			wantWorker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Obj(),
			},

			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkloadBuilder.Clone().Obj()),
					EventType: "Normal",
					Reason:    "MultiKueue",
					Message:   `The workload got reservation on "worker1"`,
				},
			},
		},
		"elastic job finished local workload via replacement is ignored": {
			features:     map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			reconcileFor: "wl1",

			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadFinished,
						Status: metav1.ConditionTrue,
						Reason: kueue.WorkloadSliceReplaced,
					}).
					ClusterName("worker1").
					Obj(),
			},
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					QuotaReservedTime(now.Add(-time.Hour)). // one hour ago
					Obj(),
			},
			worker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Obj(),
			},
			useSecondWorker: true,

			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					ClusterName("worker1").
					Condition(metav1.Condition{
						Type:   kueue.WorkloadFinished,
						Status: metav1.ConditionTrue,
						Reason: kueue.WorkloadSliceReplaced,
					}).
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
			wantWorker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Obj(),
			},
		},
		"elastic job local workload without quota reservation": {
			features:     map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			reconcileFor: "wl1",

			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ClusterName("worker1").
					Obj(),
			},
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					QuotaReservedTime(now.Add(-time.Hour)). // one hour ago
					Obj(),
			},
			worker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Obj(),
			},
			useSecondWorker: true,

			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ClusterName("worker1").
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
		},
		"elastic job local scaled-up workload slice without quota reservation": {
			features: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices: true,
			},
			reconcileFor: "wl1",

			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Annotation(workloadslicing.WorkloadSliceReplacementFor, "old-slice").
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ClusterName("worker1").
					Obj(),
			},
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					QuotaReservedTime(now.Add(-time.Hour)). // one hour ago
					Obj(),
			},
			worker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Obj(),
			},
			useSecondWorker: true,

			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Annotation(workloadslicing.WorkloadSliceReplacementFor, "old-slice").
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ClusterName("worker1").
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
			wantWorker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Obj(),
			},
		},
		"elastic job local workload out-of-sync other than scaled-down": {
			features: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices: true,
			},
			reconcileFor: "wl1",

			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					PodSets(*utiltestingapi.MakePodSet("different-name", 1).Obj()).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					ClusterName("worker1").
					Obj(),
			},
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					QuotaReservedTime(now.Add(-time.Hour)). // one hour ago
					Obj(),
			},
			worker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Obj(),
			},
			useSecondWorker: true,

			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					PodSets(*utiltestingapi.MakePodSet("different-name", 1).Obj()).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateRetry,
						Message: "Reserving remote lost",
					}).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					ClusterName("worker1").
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
		},
		"elastic job local workload out-of-sync scaled-down": {
			features: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices:      true,
				features.MultiKueueWaitForWorkloadAdmitted: false,
			},
			reconcileFor: "wl1",

			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					ClusterName("worker1").
					Obj(),
			},
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					QuotaReservedTime(now.Add(-time.Hour)). // one hour ago
					Obj(),
			},
			worker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Obj(),
			},
			useSecondWorker: true,

			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					ClusterName("worker1").
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
					Obj(),
			},
			wantWorker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkloadBuilder.Clone().Obj()),
					EventType: "Normal",
					Reason:    "MultiKueue",
					Message:   `The workload got reservation on "worker1"`,
				},
			},
		},
	}

	for name, tc := range cases {
		for _, useMergePatch := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s when the WorkloadRequestUseMergePatch feature is %t", name, useMergePatch), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, useMergePatch)

				for feature, enabled := range tc.features {
					features.SetFeatureGateDuringTest(t, feature, enabled)
				}

				ctx, _ := utiltesting.ContextWithLog(t)
				managerBuilder := getClientBuilder(ctx)
				managerBuilder = managerBuilder.WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})

				workerClusters := []string{"worker1"}
				if tc.useSecondWorker {
					workerClusters = append(workerClusters, "worker2")
				}
				managerBuilder = managerBuilder.WithLists(&kueue.WorkloadList{Items: tc.managersWorkloads}, &batchv1.JobList{Items: tc.managersJobs})
				managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersWorkloads, func(w *kueue.Workload) client.Object { return w })...)
				managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersJobs, func(w *batchv1.Job) client.Object { return w })...)
				managerBuilder = managerBuilder.WithObjects(
					utiltestingapi.MakeMultiKueueConfig("config1").Clusters(workerClusters...).Obj(),
					utiltestingapi.MakeAdmissionCheck("ac1").ControllerName(kueue.MultiKueueControllerName).
						Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
						Obj(),
				)

				managerClient := managerBuilder.Build()
				adapters, _ := jobframework.GetMultiKueueAdapters(sets.New("batch/job"))
				cRec := newClustersReconciler(managerClient, TestNamespace, 0, defaultOrigin, nil, adapters, nil, nil)

				worker1Client := getClientBuilder(ctx).
					WithLists(&kueue.WorkloadList{Items: tc.worker1Workloads}, &batchv1.JobList{Items: tc.worker1Jobs}).
					WithStatusSubresource(&kueue.Workload{}).
					WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge}).
					Build()

				w1remoteClient := newRemoteClient(managerClient, nil, nil, defaultOrigin, "", adapters)
				w1remoteClient.client = worker1Client
				w1remoteClient.connecting.Store(false)
				cRec.remoteClients["worker1"] = w1remoteClient

				var worker2Client client.WithWatch
				if tc.useSecondWorker {
					worker2Builder := getClientBuilder(ctx)
					worker2Builder = worker2Builder.WithLists(&kueue.WorkloadList{Items: tc.worker2Workloads}, &batchv1.JobList{Items: tc.worker2Jobs})
					worker2Builder = worker2Builder.WithInterceptorFuncs(interceptor.Funcs{
						Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
							if tc.worker2OnGetError != nil {
								return tc.worker2OnGetError
							}
							return c.Get(ctx, key, obj, opts...)
						},
						Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
							if tc.worker2OnCreateError != nil {
								return tc.worker2OnCreateError
							}
							return c.Create(ctx, obj, opts...)
						},
						Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
							if tc.worker2OnDeleteError != nil {
								return tc.worker2OnDeleteError
							}
							return c.Delete(ctx, obj, opts...)
						},
					})
					worker2Client = worker2Builder.Build()

					w2remoteClient := newRemoteClient(managerClient, nil, nil, defaultOrigin, "", adapters)
					w2remoteClient.client = worker2Client
					if !tc.worker2Reconnecting {
						w2remoteClient.connecting.Store(false)
					}
					cRec.remoteClients["worker2"] = w2remoteClient
				}

				helper, _ := admissioncheck.NewMultiKueueStoreHelper(managerClient)
				recorder := &utiltesting.EventRecorder{}
				mkDispatcherName := ptr.Deref(tc.dispatcherName, config.MultiKueueDispatcherModeAllAtOnce)
				reconciler := newWlReconciler(managerClient, helper, cRec, defaultOrigin, recorder, defaultWorkerLostTimeout, time.Second, adapters, mkDispatcherName, nil, WithClock(t, fakeClock))

				for _, val := range tc.managersDeletedWorkloads {
					reconciler.Delete(event.DeleteEvent{
						Object: val,
					})
				}

				_, gotErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: tc.reconcileFor, Namespace: TestNamespace}})
				if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
					t.Errorf("unexpected error (-want/+got):\n%s", diff)
				}

				if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents); diff != "" {
					t.Errorf("unexpected events (-want/+got):\n%s", diff)
				}

				// The fake client with patch.Apply cannot reset the Admission field (patch.Merge can).
				// However, other important Status fields (e.g. Conditions) still reflect the change,
				// so we deliberately ignore the Admission field here.
				if features.Enabled(features.WorkloadRequestUseMergePatch) {
					objCheckOpts = append(objCheckOpts, cmpopts.IgnoreFields(kueue.WorkloadStatus{}, "Admission", "ClusterName"))
				}

				gotManagersWorkloads := &kueue.WorkloadList{}
				if err := managerClient.List(ctx, gotManagersWorkloads); err != nil {
					t.Errorf("unexpected list manager's workloads error: %s", err)
				} else {
					// ensure deterministic comparison
					for i := range gotManagersWorkloads.Items {
						sort.Strings(gotManagersWorkloads.Items[i].Status.NominatedClusterNames)
					}
					for i := range tc.wantManagersWorkloads {
						sort.Strings(tc.wantManagersWorkloads[i].Status.NominatedClusterNames)
					}
					if diff := cmp.Diff(tc.wantManagersWorkloads, gotManagersWorkloads.Items, objCheckOpts...); diff != "" {
						t.Errorf("unexpected manager's workloads (-want/+got):\n%s", diff)
					}
				}

				gotWorker1Workloads := &kueue.WorkloadList{}
				if err := worker1Client.List(ctx, gotWorker1Workloads); err != nil {
					t.Errorf("unexpected list worker's workloads error: %s", err)
				} else {
					if diff := cmp.Diff(tc.wantWorker1Workloads, gotWorker1Workloads.Items, objCheckOpts...); diff != "" {
						t.Errorf("unexpected worker's workloads (-want/+got):\n%s", diff)
					}
				}

				gotManagersJobs := &batchv1.JobList{}
				if err := managerClient.List(ctx, gotManagersJobs); err != nil {
					t.Errorf("unexpected list manager's jobs error %s", err)
				} else {
					if diff := cmp.Diff(tc.wantManagersJobs, gotManagersJobs.Items, objCheckOpts...); diff != "" {
						t.Errorf("unexpected manager's jobs (-want/+got):\n%s", diff)
					}
				}

				gotWorker1Jobs := &batchv1.JobList{}
				if err := worker1Client.List(ctx, gotWorker1Jobs); err != nil {
					t.Error("unexpected list worker's jobs error")
				} else {
					if diff := cmp.Diff(tc.wantWorker1Jobs, gotWorker1Jobs.Items, objCheckOpts...); diff != "" {
						t.Errorf("unexpected worker's jobs (-want/+got):\n%s", diff)
					}
				}

				if tc.useSecondWorker {
					gotWorker2Workloads := &kueue.WorkloadList{}
					if err := worker2Client.List(ctx, gotWorker2Workloads); err != nil {
						t.Errorf("unexpected list worker2 workloads error: %s", err)
					} else {
						if diff := cmp.Diff(tc.wantWorker2Workloads, gotWorker2Workloads.Items, objCheckOpts...); diff != "" {
							t.Errorf("unexpected worker2 workloads (-want/+got):\n%s", diff)
						}
					}

					gotWorker2Jobs := &batchv1.JobList{}
					if err := worker2Client.List(ctx, gotWorker2Jobs); err != nil {
						t.Errorf("unexpected list worker2 jobs error: %s", err)
					} else {
						if diff := cmp.Diff(tc.wantWorker2Jobs, gotWorker2Jobs.Items, objCheckOpts...); diff != "" {
							t.Errorf("unexpected worker2 jobs (-want/+got):\n%s", diff)
						}
					}
				}

				if l := reconciler.deletedWlCache.Len(); l > 0 {
					t.Errorf("unexpected deletedWlCache len %d expecting 0", l)
				}
			})
		}
	}
}

type createCall struct {
	cluster string
	obj     *kueue.Workload
}

func TestNominateAndSynchronizeWorkers_MoreCases(t *testing.T) {
	const externalMultiKueueDispatcherController = "external.com/mk-dispatcher"

	remoteNames := make([]string, 9)
	for i := range 9 {
		remoteNames[i] = fmt.Sprintf("remote%d", i+1)
	}
	remotes := make(map[string]*kueue.Workload, len(remoteNames))
	for _, name := range remoteNames {
		remotes[name] = nil // initially no workloads on remotes
	}
	now := time.Now()

	tests := []struct {
		name             string
		dispatcherMode   string
		remotes          map[string]*kueue.Workload
		nominatedWorkers []string
		cond             *metav1.Condition
		createErr        error
		wantCreated      []string
		wantErr          bool
	}{
		{
			name:           "AllClusters: clone to all remotes, nominates all",
			dispatcherMode: config.MultiKueueDispatcherModeAllAtOnce,
			remotes:        map[string]*kueue.Workload{remoteNames[0]: nil, remoteNames[1]: nil},
			wantCreated:    []string{remoteNames[0], remoteNames[1]},
		},
		{
			name:           "AllClusters: workloads already created on remotes, do not create again",
			dispatcherMode: config.MultiKueueDispatcherModeAllAtOnce,
			remotes:        map[string]*kueue.Workload{remoteNames[0]: {}, remoteNames[1]: {}},
			wantCreated:    nil,
		},
		// Incremental dispatcher tests were moved to a separate file.
		{
			name:           "External controller: no nominated workers, nothing created",
			dispatcherMode: externalMultiKueueDispatcherController,
			remotes:        remotes,
			wantCreated:    nil,
		},
		{
			name:             "External controller: nominate remote1 and remote6",
			dispatcherMode:   externalMultiKueueDispatcherController,
			remotes:          remotes,
			nominatedWorkers: []string{remoteNames[0], remoteNames[5]},
			wantCreated:      []string{remoteNames[0], remoteNames[5]},
		},
		{
			name:             "External controller: nominate all remotes at once",
			dispatcherMode:   externalMultiKueueDispatcherController,
			remotes:          remotes,
			nominatedWorkers: remoteNames,
			wantCreated:      remoteNames,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClock := testingclock.NewFakeClock(now)

			local := &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: "wl", Namespace: "ns"},
				Status: kueue.WorkloadStatus{
					Conditions:            make([]metav1.Condition, 0, 1),
					NominatedClusterNames: tt.nominatedWorkers,
				},
			}

			var created []createCall
			makeFakeCreate := func(origin string) func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				return func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					created = append(created, createCall{
						cluster: origin,
						obj:     obj.(*kueue.Workload),
					})
					return c.Create(ctx, obj, opts...)
				}
			}
			objs := []client.Object{local}
			wlClientBuilder := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					local.Status.NominatedClusterNames = obj.(*kueue.Workload).Status.NominatedClusterNames
					return utiltesting.TreatSSAAsStrategicMerge(ctx, client, subResourceName, obj, patch, opts...)
				},
			}).WithObjects(objs...).WithStatusSubresource(objs...)

			remoteClientBuilders := make(map[string]*fake.ClientBuilder, len(tt.remotes))
			for remote := range tt.remotes {
				remoteClientBuilders[remote] = utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
					Create: makeFakeCreate(remote),
				},
				)
			}

			remoteClients := make(map[string]*remoteClient, len(tt.remotes))
			for remote, builder := range remoteClientBuilders {
				remoteClients[remote] = &remoteClient{client: builder.Build(), origin: remote}
			}

			if tt.cond != nil {
				local.Status.Conditions = append(local.Status.Conditions, *tt.cond)
			}
			group := &wlGroup{
				local:         local,
				remotes:       tt.remotes,
				remoteClients: remoteClients,
				acName:        "ac1",
			}

			wlRec := &wlReconciler{
				clock:          fakeClock,
				dispatcherName: tt.dispatcherMode,
				client:         wlClientBuilder.Build(),
			}

			ctx, _ := utiltesting.ContextWithLog(t)
			_, err := wlRec.nominateAndSynchronizeWorkers(ctx, group)
			if (err != nil) != tt.wantErr {
				t.Errorf("expected error: %v, got: %v", tt.wantErr, err)
			}

			var gotCreated []string
			for _, c := range created {
				gotCreated = append(gotCreated, c.cluster)
			}
			s1 := sort.StringSlice(tt.wantCreated)
			s1.Sort()
			s2 := sort.StringSlice(gotCreated)
			s2.Sort()
			if diff := cmp.Diff(s1, s2); diff != "" {
				t.Errorf("unexpected created remotes (-want/+got):\n%s", diff)
			}
		})
	}
}

// mockQueue implements workqueue.TypedRateLimitingInterface for testing
type mockQueue struct {
	addedItems []reconcile.Request
}

func (m *mockQueue) Add(item reconcile.Request) {
	m.addedItems = append(m.addedItems, item)
}

func (m *mockQueue) Len() int                          { return 0 }
func (m *mockQueue) Get() (reconcile.Request, bool)    { return reconcile.Request{}, false }
func (m *mockQueue) Done(reconcile.Request)            {}
func (m *mockQueue) Forget(reconcile.Request)          {}
func (m *mockQueue) NumRequeues(reconcile.Request) int { return 0 }
func (m *mockQueue) AddRateLimited(reconcile.Request)  {}
func (m *mockQueue) AddAfter(item reconcile.Request, duration time.Duration) {
	m.addedItems = append(m.addedItems, item)
}
func (m *mockQueue) ShutDown()          {}
func (m *mockQueue) ShutDownWithDrain() {}
func (m *mockQueue) ShuttingDown() bool { return false }

func TestConfigHandlerUpdate(t *testing.T) {
	cases := map[string]struct {
		admissionChecks   []kueue.AdmissionCheck
		workloads         []kueue.Workload
		oldConfig         *kueue.MultiKueueConfig
		newConfig         *kueue.MultiKueueConfig
		expectedQueuedWLs []string
	}{
		"clusters unchanged - no workloads queued": {
			admissionChecks: []kueue.AdmissionCheck{
				*utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", TestNamespace).
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					Obj(),
			},
			oldConfig:         utiltestingapi.MakeMultiKueueConfig("config1").Clusters("cluster1", "cluster2").Obj(),
			newConfig:         utiltestingapi.MakeMultiKueueConfig("config1").Clusters("cluster1", "cluster2").Obj(),
			expectedQueuedWLs: nil, // No workloads should be queued
		},
		"clusters changed - workloads queued": {
			admissionChecks: []kueue.AdmissionCheck{
				*utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", TestNamespace).
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", TestNamespace).
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStateReady}).
					Obj(),
			},
			oldConfig:         utiltestingapi.MakeMultiKueueConfig("config1").Clusters("cluster1").Obj(),
			newConfig:         utiltestingapi.MakeMultiKueueConfig("config1").Clusters("cluster1", "cluster2").Obj(),
			expectedQueuedWLs: []string{"wl1", "wl2"}, // Both workloads should be queued
		},
		"multiple configs - only affected workloads queued": {
			admissionChecks: []kueue.AdmissionCheck{
				*utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
				*utiltestingapi.MakeAdmissionCheck("ac2").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "other-config").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", TestNamespace).
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", TestNamespace).
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac2", State: kueue.CheckStatePending}).
					Obj(),
			},
			oldConfig:         utiltestingapi.MakeMultiKueueConfig("config1").Clusters("cluster1").Obj(),
			newConfig:         utiltestingapi.MakeMultiKueueConfig("config1").Clusters("cluster1", "cluster2").Obj(),
			expectedQueuedWLs: []string{"wl1"}, // Only wl1 uses config1, wl2 uses other-config
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := getClientBuilder(ctx)

			for i := range tc.admissionChecks {
				clientBuilder = clientBuilder.WithObjects(&tc.admissionChecks[i])
			}
			for i := range tc.workloads {
				clientBuilder = clientBuilder.WithObjects(&tc.workloads[i])
			}

			fakeClient := clientBuilder.Build()
			handler := &configHandler{client: fakeClient, eventsBatchPeriod: time.Second}
			mockQ := &mockQueue{}

			updateEvent := event.UpdateEvent{
				ObjectOld: tc.oldConfig,
				ObjectNew: tc.newConfig,
			}

			handler.Update(ctx, updateEvent, mockQ)

			var actualQueuedWLs []string
			for _, req := range mockQ.addedItems {
				actualQueuedWLs = append(actualQueuedWLs, req.Name)
			}
			sort.Strings(actualQueuedWLs)
			sort.Strings(tc.expectedQueuedWLs)

			if diff := cmp.Diff(tc.expectedQueuedWLs, actualQueuedWLs); diff != "" {
				t.Errorf("unexpected queued workloads (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestConfigHandlerDelete(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)

	admissionCheck := utiltestingapi.MakeAdmissionCheck("ac1").
		ControllerName(kueue.MultiKueueControllerName).
		Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
		Obj()

	workload := utiltestingapi.MakeWorkload("wl1", TestNamespace).
		AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
		Obj()

	clientBuilder := getClientBuilder(ctx)
	clientBuilder = clientBuilder.WithObjects(admissionCheck, workload)
	fakeClient := clientBuilder.Build()

	handler := &configHandler{client: fakeClient, eventsBatchPeriod: time.Second}
	mockQ := &mockQueue{}

	config := utiltestingapi.MakeMultiKueueConfig("config1").Clusters("cluster1").Obj()
	deleteEvent := event.DeleteEvent{
		Object: config,
	}

	handler.Delete(ctx, deleteEvent, mockQ)

	if len(mockQ.addedItems) != 1 {
		t.Errorf("expected 1 workload to be queued, got %d", len(mockQ.addedItems))
	}
	if mockQ.addedItems[0].Name != "wl1" {
		t.Errorf("expected workload wl1 to be queued, got %s", mockQ.addedItems[0].Name)
	}
}
