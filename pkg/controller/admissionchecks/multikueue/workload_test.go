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
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"

	// To ensure the integration manager gets populated
	_ "sigs.k8s.io/kueue/pkg/controller/jobs"
)

var (
	errFake = errors.New("fake error")
)

func TestWlReconcile(t *testing.T) {
	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)

	objCheckOpts := cmp.Options{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
		cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime"),
		cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
	}

	baseWorkloadBuilder := utiltesting.MakeWorkload("wl1", TestNamespace)
	baseJobBuilder := testingjob.MakeJob("job1", TestNamespace).Suspend(false)
	baseJobManagedByKueueBuilder := baseJobBuilder.Clone().ManagedBy(kueue.MultiKueueControllerName)

	cases := map[string]struct {
		reconcileFor             string
		managersWorkloads        []kueue.Workload
		managersJobs             []batchv1.Job
		managersDeletedWorkloads []*kueue.Workload
		worker1Workloads         []kueue.Workload
		worker1Jobs              []batchv1.Job
		withoutJobManagedBy      bool
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},
			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
		"wl without reservation, clears the workload objects (withoutJobManagedBy)": {
			reconcileFor:        "wl1",
			withoutJobManagedBy: true,
			managersJobs:        []batchv1.Job{*baseJobBuilder.Clone().Obj()},
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
			wantManagersJobs: []batchv1.Job{*baseJobBuilder.Clone().Obj()},
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},
			useSecondWorker:      true,
			worker2OnCreateError: errFake,

			wantManagersJobs: []batchv1.Job{*baseJobManagedByKueueBuilder.Clone().Obj()},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					NominatedClusterNames([]string{"worker1", "worker2"}).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					NominatedClusterNames([]string{"worker1", "worker2"}).
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
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},

			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},

			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},

			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},
			wantWorker2Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Obj(),
			},
			wantError: errFake,
		},
		"remote wl with reservation": {
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},

			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},

			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					ClusterName("worker1").
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},

			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
		"remote wl with reservation (withoutJobManagedBy)": {
			reconcileFor:        "wl1",
			withoutJobManagedBy: true,
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},

			managersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().Obj(),
			},

			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
						State:   kueue.CheckStatePending,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					ClusterName("worker1").
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().Obj(),
			},

			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
		"remote wl with reservation (withoutJobManagedBy, MultiKueueDispatcherModeIncremental)": {
			reconcileFor:        "wl1",
			withoutJobManagedBy: true,
			dispatcherName:      ptr.To(config.MultiKueueDispatcherModeIncremental),
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},

			managersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().Obj(),
			},

			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
						State:   kueue.CheckStatePending,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					ClusterName("worker1").
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().Obj(),
			},

			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
		"remote job is changing status the local Job is updated ": {
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
		"remote job is changing status, the local job is not updated (withoutJobManagedBy)": {
			reconcileFor:        "wl1",
			withoutJobManagedBy: true,
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStatePending,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},

			managersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().Obj(),
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					ClusterName("worker1").
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().Obj(),
			},

			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
		"remote wl is finished, the local workload and Job are marked completed (withoutJobManagedBy)": {
			reconcileFor:        "wl1",
			withoutJobManagedBy: true,
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStatePending,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},

			managersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().Obj(),
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "ByTest", Message: `by test`}).
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
					Obj(),
			},

			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},
			worker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Obj(),
			},
		},
		"worker reconnects after the local workload is requeued and got reservation on a second worker": {
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					ClusterName("worker1").
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.MultiKueueBatchJobWithManagedBy, !tc.withoutJobManagedBy)
			managerBuilder := getClientBuilder(t.Context())
			managerBuilder = managerBuilder.WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})

			workerClusters := []string{"worker1"}
			if tc.useSecondWorker {
				workerClusters = append(workerClusters, "worker2")
			}
			managerBuilder = managerBuilder.WithLists(&kueue.WorkloadList{Items: tc.managersWorkloads}, &batchv1.JobList{Items: tc.managersJobs})
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersWorkloads, func(w *kueue.Workload) client.Object { return w })...)
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersJobs, func(w *batchv1.Job) client.Object { return w })...)
			managerBuilder = managerBuilder.WithObjects(
				utiltesting.MakeMultiKueueConfig("config1").Clusters(workerClusters...).Obj(),
				utiltesting.MakeAdmissionCheck("ac1").ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			)

			managerClient := managerBuilder.Build()
			adapters, _ := jobframework.GetMultiKueueAdapters(sets.New("batch/job"))
			cRec := newClustersReconciler(managerClient, TestNamespace, 0, defaultOrigin, nil, adapters)

			worker1Builder := getClientBuilder(t.Context())
			worker1Builder = worker1Builder.WithLists(&kueue.WorkloadList{Items: tc.worker1Workloads}, &batchv1.JobList{Items: tc.worker1Jobs})
			worker1Client := worker1Builder.Build()

			w1remoteClient := newRemoteClient(managerClient, nil, nil, defaultOrigin, "", adapters)
			w1remoteClient.client = worker1Client
			w1remoteClient.connecting.Store(false)
			cRec.remoteClients["worker1"] = w1remoteClient

			var worker2Client client.WithWatch
			if tc.useSecondWorker {
				worker2Builder := getClientBuilder(t.Context())
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

			helper, _ := newMultiKueueStoreHelper(managerClient)
			recorder := &utiltesting.EventRecorder{}
			mkDispatcherName := ptr.Deref(tc.dispatcherName, config.MultiKueueDispatcherModeAllAtOnce)
			reconciler := newWlReconciler(managerClient, helper, cRec, defaultOrigin, recorder, defaultWorkerLostTimeout, time.Second, adapters, mkDispatcherName, WithClock(t, fakeClock))

			for _, val := range tc.managersDeletedWorkloads {
				reconciler.Delete(event.DeleteEvent{
					Object: val,
				})
			}

			_, gotErr := reconciler.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Name: tc.reconcileFor, Namespace: TestNamespace}})
			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents); diff != "" {
				t.Errorf("unexpected events (-want/+got):\n%s", diff)
			}

			gotManagersWorkloads := &kueue.WorkloadList{}
			if err := managerClient.List(t.Context(), gotManagersWorkloads); err != nil {
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
			if err := worker1Client.List(t.Context(), gotWorker1Workloads); err != nil {
				t.Errorf("unexpected list worker's workloads error: %s", err)
			} else {
				if diff := cmp.Diff(tc.wantWorker1Workloads, gotWorker1Workloads.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected worker's workloads (-want/+got):\n%s", diff)
				}
			}

			gotManagersJobs := &batchv1.JobList{}
			if err := managerClient.List(t.Context(), gotManagersJobs); err != nil {
				t.Errorf("unexpected list manager's jobs error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantManagersJobs, gotManagersJobs.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected manager's jobs (-want/+got):\n%s", diff)
				}
			}

			gotWorker1Jobs := &batchv1.JobList{}
			if err := worker1Client.List(t.Context(), gotWorker1Jobs); err != nil {
				t.Error("unexpected list worker's jobs error")
			} else {
				if diff := cmp.Diff(tc.wantWorker1Jobs, gotWorker1Jobs.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected worker's jobs (-want/+got):\n%s", diff)
				}
			}

			if tc.useSecondWorker {
				gotWorker2Workloads := &kueue.WorkloadList{}
				if err := worker2Client.List(t.Context(), gotWorker2Workloads); err != nil {
					t.Errorf("unexpected list worker2 workloads error: %s", err)
				} else {
					if diff := cmp.Diff(tc.wantWorker2Workloads, gotWorker2Workloads.Items, objCheckOpts...); diff != "" {
						t.Errorf("unexpected worker2 workloads (-want/+got):\n%s", diff)
					}
				}

				gotWorker2Jobs := &batchv1.JobList{}
				if err := worker2Client.List(t.Context(), gotWorker2Jobs); err != nil {
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
		advanceClock     time.Duration
		wantRetryAfter   time.Duration
	}{
		{
			name:           "AllClusters: clone to all remotes, nominates all",
			dispatcherMode: config.MultiKueueDispatcherModeAllAtOnce,
			remotes:        map[string]*kueue.Workload{remoteNames[0]: nil, remoteNames[1]: nil},
			wantCreated:    []string{remoteNames[0], remoteNames[1]},
			wantRetryAfter: 0,
		},
		{
			name:           "AllClusters: workloads already created on remotes, do not create again",
			dispatcherMode: config.MultiKueueDispatcherModeAllAtOnce,
			remotes:        map[string]*kueue.Workload{remoteNames[0]: {}, remoteNames[1]: {}},
			wantCreated:    nil,
			wantRetryAfter: 0,
		},
		{
			name:           "Incremental: only one remote, nominates it",
			dispatcherMode: config.MultiKueueDispatcherModeIncremental,
			remotes:        map[string]*kueue.Workload{remoteNames[0]: nil},
			wantCreated:    []string{remoteNames[0]},
			wantRetryAfter: 0,
		},
		{
			name:             "Incremental: no previous nomination, nominates remote1...3",
			dispatcherMode:   config.MultiKueueDispatcherModeIncremental,
			remotes:          remotes,
			nominatedWorkers: []string{""},
			wantCreated:      remoteNames[:3],
			wantRetryAfter:   0,
		},
		{
			name:             "Incremental: keep remote1..3, nomination round still in progress",
			dispatcherMode:   config.MultiKueueDispatcherModeIncremental,
			remotes:          remotes,
			nominatedWorkers: remoteNames[:3],
			wantCreated:      nil,
			advanceClock:     incrementalDispatcherRoundTimeout / 2,
			wantRetryAfter:   incrementalDispatcherRoundTimeout / 2,
		},
		{
			name:             "Incremental: previously remote1..3, nomination round exceeded - increment",
			dispatcherMode:   config.MultiKueueDispatcherModeIncremental,
			remotes:          remotes,
			nominatedWorkers: remoteNames[:3],
			advanceClock:     incrementalDispatcherRoundTimeout + time.Second,
			wantCreated:      remoteNames[:6],
			wantRetryAfter:   0,
		},
		{
			name:             "Incremental: previously remote1..6, next nomination round all clusters",
			dispatcherMode:   config.MultiKueueDispatcherModeIncremental,
			remotes:          remotes,
			nominatedWorkers: remoteNames[:6],
			advanceClock:     incrementalDispatcherRoundTimeout + time.Second,
			wantCreated:      remoteNames,
			wantRetryAfter:   0,
		},
		{
			name:           "External controller: no nominated workers, nothing created",
			dispatcherMode: externalMultiKueueDispatcherController,
			remotes:        remotes,
			wantCreated:    nil,
			wantRetryAfter: 0,
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
				clock:           fakeClock,
				dispatcherName:  tt.dispatcherMode,
				client:          wlClientBuilder.Build(),
				roundStartTimes: make(map[types.NamespacedName]time.Time),
			}

			if tt.advanceClock > 0 {
				key := types.NamespacedName{Name: group.local.Name, Namespace: group.local.Namespace}
				wlRec.roundStartTimes[key] = fakeClock.Now()
				fakeClock.SetTime(now.Add(tt.advanceClock))
			}

			res, err := wlRec.nominateAndSynchronizeWorkers(t.Context(), group)
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

			const timeMargin = 100 * time.Millisecond
			if tt.wantRetryAfter == 0 {
				if res.RequeueAfter != 0 {
					t.Errorf("unexpected RequeueAfter, want %v, got %v", tt.wantRetryAfter, res.RequeueAfter)
				}
			} else {
				if res.RequeueAfter < tt.wantRetryAfter-timeMargin || res.RequeueAfter > tt.wantRetryAfter+timeMargin {
					t.Errorf("unexpected RequeueAfter, want %v±%v, got %v", tt.wantRetryAfter, timeMargin, res.RequeueAfter)
				}
			}
		})
	}
}
