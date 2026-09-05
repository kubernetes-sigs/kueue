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
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
)

type retentionTestAdapter struct {
	gvk schema.GroupVersionKind
}

var _ jobframework.MultiKueueAdapter = retentionTestAdapter{}

func (retentionTestAdapter) SyncJob(context.Context, client.Client, client.Client, types.NamespacedName, string, string) (bool, error) {
	return false, nil
}

func (a retentionTestAdapter) DeleteRemoteObject(ctx context.Context, _ client.Client, remoteClient client.Client, key types.NamespacedName) error {
	var object client.Object = &batchv1.Job{}
	if a.GVK().GroupKind() == corev1.SchemeGroupVersion.WithKind("Pod").GroupKind() {
		object = &corev1.Pod{}
	}
	object.SetName(key.Name)
	object.SetNamespace(key.Namespace)
	return client.IgnoreNotFound(remoteClient.Delete(ctx, object))
}

func (retentionTestAdapter) IsJobManagedByKueue(context.Context, client.Client, types.NamespacedName) (bool, string, error) {
	return true, "", nil
}

func (a retentionTestAdapter) GVK() schema.GroupVersionKind {
	if !a.gvk.Empty() {
		return a.gvk
	}
	return batchv1.SchemeGroupVersion.WithKind("Job")
}

func TestRemoteRetentionRemaining(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	tests := map[string]struct {
		gate      bool
		duration  time.Duration
		condition *metav1.Condition
		want      time.Duration
	}{
		"succeeded Workload is retained": {
			gate:     true,
			duration: 10 * time.Minute,
			condition: &metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue,
				Reason: kueue.WorkloadFinishedReasonSucceeded, LastTransitionTime: metav1.NewTime(now.Add(-time.Minute))},
			want: 9 * time.Minute,
		},
		"failed Workload is retained": {
			gate:     true,
			duration: 10 * time.Minute,
			condition: &metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue,
				Reason: kueue.WorkloadFinishedReasonFailed, LastTransitionTime: metav1.NewTime(now)},
			want: 10 * time.Minute,
		},
		"retention elapsed": {
			gate:     true,
			duration: time.Minute,
			condition: &metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue,
				Reason: kueue.WorkloadFinishedReasonSucceeded, LastTransitionTime: metav1.NewTime(now.Add(-time.Minute))},
		},
		"non-completion reason is not retained": {
			gate:     true,
			duration: 10 * time.Minute,
			condition: &metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue,
				Reason: kueue.WorkloadFinishedReasonOutOfSync, LastTransitionTime: metav1.NewTime(now)},
		},
		"disabled gate keeps immediate cleanup": {
			duration: 10 * time.Minute,
			condition: &metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue,
				Reason: kueue.WorkloadFinishedReasonSucceeded, LastTransitionTime: metav1.NewTime(now)},
		},
		"zero duration keeps immediate cleanup": {
			gate: true,
			condition: &metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue,
				Reason: kueue.WorkloadFinishedReasonSucceeded, LastTransitionTime: metav1.NewTime(now)},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.MultiKueueRemoteObjectRetention, tc.gate)
			wl := &kueue.Workload{}
			if tc.condition != nil {
				wl.Status.Conditions = []metav1.Condition{*tc.condition}
			}
			reconciler := &wlReconciler{clock: testingclock.NewFakeClock(now), remoteObjectsAfterFinished: tc.duration}
			if got := reconciler.remoteRetentionRemaining(wl); got != tc.want {
				t.Fatalf("remoteRetentionRemaining() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReconcileGroupRemoteRetention(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.WorkloadIdentifierAnnotations, true)

	now := time.Now().Truncate(time.Second)
	tests := map[string]struct {
		gate              bool
		withQuota         bool
		evicted           bool
		inactive          bool
		selectedEvicted   bool
		selectedOutOfSync bool
		wantRetained      bool
		wantRequeueAfter  time.Duration
	}{
		"retains only the finishing worker": {
			gate:             true,
			withQuota:        true,
			wantRetained:     true,
			wantRequeueAfter: 9 * time.Minute,
		},
		"quota loss remains immediate": {
			gate:         true,
			wantRetained: false,
		},
		"completion racing with manager eviction keeps cleanup immediate": {
			gate:      true,
			withQuota: true,
			evicted:   true,
		},
		"completion racing with worker eviction keeps cleanup immediate": {
			gate:            true,
			withQuota:       true,
			selectedEvicted: true,
		},
		"deactivated finished Workload keeps cleanup immediate": {
			gate:      true,
			withQuota: true,
			inactive:  true,
		},
		"out-of-sync recovery remains immediate": {
			gate:              true,
			withQuota:         true,
			selectedOutOfSync: true,
		},
		"disabled gate keeps immediate cleanup": {
			withQuota:    true,
			wantRetained: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.MultiKueueRemoteObjectRetention, tc.gate)
			ctx, _ := utiltesting.ContextWithLog(t)
			localBuilder := utiltestingapi.MakeWorkload("wl", TestNamespace).ClusterName("worker1")
			if tc.withQuota {
				localBuilder = localBuilder.ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now)
			}
			if tc.evicted {
				localBuilder.EvictedAt(now)
			}
			localBuilder.Active(!tc.inactive)
			localBuilder = localBuilder.Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue,
				Reason: kueue.WorkloadFinishedReasonSucceeded, LastTransitionTime: metav1.NewTime(now.Add(-time.Minute))})
			local := localBuilder.Obj()
			selectedRemote := utiltestingapi.MakeWorkload(local.Name, local.Namespace).Obj()
			local.Spec.DeepCopyInto(&selectedRemote.Spec)
			nonSelectedRemote := selectedRemote.DeepCopy()
			if tc.selectedOutOfSync {
				selectedRemote.Spec.QueueName = "other"
			}
			if tc.selectedEvicted {
				workloadevict.SetEvictedCondition(selectedRemote, now, kueue.WorkloadEvictedByPreemption, "preempted")
			}
			remoteJob := func() *batchv1.Job {
				return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "job", Namespace: TestNamespace,
					Labels:      map[string]string{kueue.MultiKueueOriginLabel: defaultOrigin},
					Annotations: map[string]string{constants.PrebuiltWorkloadAnnotation: local.Name}}}
			}

			managerClient := getClientBuilder(ctx).Build()
			worker1Client := getClientBuilder(ctx).WithObjects(selectedRemote, remoteJob()).Build()
			worker2Client := getClientBuilder(ctx).WithObjects(nonSelectedRemote, remoteJob()).Build()
			adapter := retentionTestAdapter{}
			worker1 := newRemoteClient(managerClient, nil, nil, nil, defaultOrigin, "worker1", nil)
			worker1.client = NewNeverCachingClient(worker1Client)
			worker2 := newRemoteClient(managerClient, nil, nil, nil, defaultOrigin, "worker2", nil)
			worker2.client = NewNeverCachingClient(worker2Client)

			group := &wlGroup{
				local: local, localClient: managerClient,
				remotes:       map[string]*kueue.Workload{"worker1": selectedRemote, "worker2": nonSelectedRemote},
				remoteClients: map[string]*remoteClient{"worker1": worker1, "worker2": worker2},
				jobAdapter:    adapter, controllerKey: types.NamespacedName{Name: "job", Namespace: TestNamespace},
			}
			reconciler := &wlReconciler{clock: testingclock.NewFakeClock(now), remoteObjectsAfterFinished: 10 * time.Minute}
			result, err := reconciler.reconcileGroup(ctx, group)
			if err != nil {
				t.Fatalf("reconcileGroup() error = %v", err)
			}
			if result.RequeueAfter != tc.wantRequeueAfter {
				t.Fatalf("reconcileGroup() RequeueAfter = %v, want %v", result.RequeueAfter, tc.wantRequeueAfter)
			}

			for workerName, workerClient := range map[string]client.Client{"worker1": worker1Client, "worker2": worker2Client} {
				wantPresent := workerName == "worker1" && tc.wantRetained
				objects := []struct {
					key types.NamespacedName
					obj client.Object
				}{
					{key: client.ObjectKeyFromObject(local), obj: &kueue.Workload{}},
					{key: types.NamespacedName{Name: "job", Namespace: TestNamespace}, obj: &batchv1.Job{}},
				}
				for _, object := range objects {
					err := workerClient.Get(ctx, object.key, object.obj)
					if wantPresent && err != nil {
						t.Fatalf("%s object %T was deleted: %v", workerName, object.obj, err)
					}
					if !wantPresent && !apierrors.IsNotFound(err) {
						t.Fatalf("%s object %T still exists, error = %v", workerName, object.obj, err)
					}
				}
			}
		})
	}
}

func TestSameNameReplacementChecksOwnershipAndCurrentManager(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.WorkloadIdentifierAnnotations, true)

	const currentManagerUID = types.UID("current-manager")
	tests := map[string]struct {
		workloadManagerUID  types.UID
		remoteObjectOrigin  string
		remoteObjectPresent bool
		emptyOrigin         bool
		wantDeleted         bool
		wantRemoteObject    bool
		wantRemoteWorkload  bool
		wantErr             error
	}{
		"gate-off current Workload deletes the old object on the target worker": {
			workloadManagerUID:  currentManagerUID,
			remoteObjectOrigin:  defaultOrigin,
			remoteObjectPresent: true,
			wantDeleted:         true,
		},
		"stale Workload preserves the newer run object": {
			workloadManagerUID:  "old-manager",
			remoteObjectOrigin:  defaultOrigin,
			remoteObjectPresent: true,
			wantRemoteObject:    true,
		},
		"stale Workload does not create a remote Workload when the remote object is absent": {
			workloadManagerUID: "old-manager",
		},
		"foreign-origin object is preserved and remote Workload is created": {
			workloadManagerUID:  currentManagerUID,
			remoteObjectOrigin:  "other-origin",
			remoteObjectPresent: true,
			wantRemoteObject:    true,
			wantRemoteWorkload:  true,
		},
		"empty origin fails closed": {
			workloadManagerUID:  currentManagerUID,
			remoteObjectPresent: true,
			emptyOrigin:         true,
			wantRemoteObject:    true,
			wantErr:             jobframework.ErrMultiKueueOriginEmpty,
		},
		"missing manager UID skips deletion and still creates the remote Workload": {
			remoteObjectOrigin:  defaultOrigin,
			remoteObjectPresent: true,
			wantRemoteObject:    true,
			wantRemoteWorkload:  true,
		},
		"missing manager UID still creates the remote Workload": {
			wantRemoteWorkload: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			managerJob := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "job", Namespace: TestNamespace, UID: currentManagerUID}}
			managerClient := getClientBuilder(ctx).WithObjects(managerJob).Build()
			oldRemoteJob := func() *batchv1.Job {
				job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: managerJob.Name, Namespace: managerJob.Namespace,
					Annotations: map[string]string{constants.PrebuiltWorkloadAnnotation: "old-workload"}}}
				if !tc.emptyOrigin {
					job.Labels = map[string]string{kueue.MultiKueueOriginLabel: tc.remoteObjectOrigin}
				}
				return job
			}
			worker1Builder := getClientBuilder(ctx)
			worker2Builder := getClientBuilder(ctx)
			if tc.remoteObjectPresent {
				worker1Builder = worker1Builder.WithObjects(oldRemoteJob())
				worker2Builder = worker2Builder.WithObjects(oldRemoteJob())
			}
			worker1Client := worker1Builder.Build()
			worker2Client := worker2Builder.Build()
			origin := defaultOrigin
			if tc.emptyOrigin {
				origin = ""
			}
			worker1 := newRemoteClient(managerClient, nil, nil, nil, origin, "worker1", nil)
			worker1.client = NewNeverCachingClient(worker1Client)
			worker2 := newRemoteClient(managerClient, nil, nil, nil, origin, "worker2", nil)
			worker2.client = NewNeverCachingClient(worker2Client)

			local := utiltestingapi.MakeWorkload("new-workload", TestNamespace).
				Label(constants.JobUIDLabel, string(tc.workloadManagerUID)).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), managerJob.Name, string(tc.workloadManagerUID)).
				Obj()
			group := &wlGroup{
				local: local, localClient: managerClient,
				remotes:       map[string]*kueue.Workload{"worker1": nil, "worker2": nil},
				remoteClients: map[string]*remoteClient{"worker1": worker1, "worker2": worker2},
				jobAdapter:    retentionTestAdapter{}, controllerKey: client.ObjectKeyFromObject(managerJob),
			}

			result, err := (&wlReconciler{}).syncToSingleCluster(ctx, klog.Background(), group, "worker2")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("syncToSingleCluster() error = %v, want %v", err, tc.wantErr)
			}
			wantRequeueAfter := time.Duration(0)
			if tc.wantDeleted {
				wantRequeueAfter = remoteObjectReplacementRequeueAfter
			}
			if result.RequeueAfter != wantRequeueAfter {
				t.Fatalf("syncToSingleCluster() RequeueAfter = %v, want %v", result.RequeueAfter, wantRequeueAfter)
			}
			err = worker1Client.Get(ctx, client.ObjectKeyFromObject(managerJob), &batchv1.Job{})
			if tc.remoteObjectPresent && err != nil {
				t.Fatalf("non-target worker object was deleted: %v", err)
			}
			if !tc.remoteObjectPresent && !apierrors.IsNotFound(err) {
				t.Fatalf("non-target worker object was unexpectedly created, error = %v", err)
			}
			err = worker2Client.Get(ctx, client.ObjectKeyFromObject(managerJob), &batchv1.Job{})
			if tc.wantRemoteObject && err != nil {
				t.Fatalf("target worker object was unexpectedly deleted: %v", err)
			}
			if !tc.wantRemoteObject && !apierrors.IsNotFound(err) {
				t.Fatalf("target worker object still exists, error = %v", err)
			}
			err = worker2Client.Get(ctx, client.ObjectKeyFromObject(local), &kueue.Workload{})
			if tc.wantRemoteWorkload && err != nil {
				t.Fatalf("remote Workload was not created: %v", err)
			}
			if !tc.wantRemoteWorkload && !apierrors.IsNotFound(err) {
				t.Fatalf("remote Workload was created before replacement completed, error = %v", err)
			}
		})
	}
}

func TestSameNameReplacementSkipsPodGroups(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.WorkloadIdentifierAnnotations, true)
	ctx, _ := utiltesting.ContextWithLog(t)
	local := utiltestingapi.MakeWorkload("new-group", TestNamespace).
		Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
		Obj()
	remotePod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "pod", Namespace: TestNamespace,
		Labels:      map[string]string{kueue.MultiKueueOriginLabel: defaultOrigin},
		Annotations: map[string]string{constants.PrebuiltWorkloadAnnotation: "old-group"},
	}}
	managerClient := getClientBuilder(ctx).Build()
	workerClient := getClientBuilder(ctx).WithObjects(remotePod).Build()
	worker := newRemoteClient(managerClient, nil, nil, nil, defaultOrigin, "worker", nil)
	worker.client = NewNeverCachingClient(workerClient)
	group := &wlGroup{
		local: local, localClient: managerClient,
		remoteClients: map[string]*remoteClient{"worker": worker},
		jobAdapter:    retentionTestAdapter{gvk: corev1.SchemeGroupVersion.WithKind("Pod")},
		controllerKey: client.ObjectKeyFromObject(remotePod),
	}

	deleted, proceed, err := group.deleteRemoteObjectOfOtherWorkload(ctx, "worker")
	if err != nil {
		t.Fatalf("deleteRemoteObjectOfOtherWorkload() error = %v", err)
	}
	if deleted || !proceed {
		t.Fatalf("deleteRemoteObjectOfOtherWorkload() = (deleted: %t, proceed: %t), want false, true", deleted, proceed)
	}
	if err := workerClient.Get(ctx, client.ObjectKeyFromObject(remotePod), &corev1.Pod{}); err != nil {
		t.Fatalf("remote Pod was deleted: %v", err)
	}
}

func TestSameNameReplacementPreservesConcurrentOwnershipChange(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.WorkloadIdentifierAnnotations, true)
	ctx, _ := utiltesting.ContextWithLog(t)
	managerJob := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "job", Namespace: TestNamespace, UID: "manager"}}
	managerClient := getClientBuilder(ctx).WithObjects(managerJob).Build()
	remoteJob := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name: managerJob.Name, Namespace: managerJob.Namespace, UID: "remote",
		Labels:      map[string]string{kueue.MultiKueueOriginLabel: defaultOrigin},
		Annotations: map[string]string{constants.PrebuiltWorkloadAnnotation: "old-workload"},
	}}
	workerClient := getClientBuilder(ctx).WithObjects(remoteJob).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				// Ownership changes after the reconciler reads the old metadata.
				current := &batchv1.Job{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(remoteJob), current); err != nil {
					return err
				}
				current.Labels[kueue.MultiKueueOriginLabel] = "other-origin"
				if err := c.Update(ctx, current); err != nil {
					return err
				}
				return c.Delete(ctx, obj, opts...)
			},
		}).Build()
	worker := newRemoteClient(managerClient, nil, nil, nil, defaultOrigin, "worker", nil)
	worker.client = NewNeverCachingClient(workerClient)
	local := utiltestingapi.MakeWorkload("new-workload", TestNamespace).
		ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), managerJob.Name, string(managerJob.UID)).Obj()
	group := &wlGroup{
		local: local, localClient: managerClient,
		remotes:       map[string]*kueue.Workload{"worker": nil},
		remoteClients: map[string]*remoteClient{"worker": worker},
		jobAdapter:    retentionTestAdapter{}, controllerKey: client.ObjectKeyFromObject(managerJob),
	}

	_, err := (&wlReconciler{}).syncToSingleCluster(ctx, klog.Background(), group, "worker")
	if !apierrors.IsConflict(err) {
		t.Errorf("syncToSingleCluster() error = %v, want Conflict", err)
	}
	if err := workerClient.Get(ctx, client.ObjectKeyFromObject(remoteJob), &batchv1.Job{}); err != nil {
		t.Fatalf("remote Job with changed ownership was deleted: %v", err)
	}
	if err := workerClient.Get(ctx, client.ObjectKeyFromObject(local), &kueue.Workload{}); !apierrors.IsNotFound(err) {
		t.Fatalf("remote Workload was created before resolving the conflict, error = %v", err)
	}
}
