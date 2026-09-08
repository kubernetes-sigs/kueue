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

package jobframework

import (
	"context"
	"errors"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

type createRaceAdapter struct{}

func jobBoundWorkload(name string, key types.NamespacedName, managerUID types.UID) *kueue.Workload {
	return &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
		Name:      name,
		Namespace: key.Namespace,
		UID:       types.UID("manager-" + name + "-uid"),
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: batchv1.SchemeGroupVersion.String(),
			Kind:       "Job",
			Name:       key.Name,
			UID:        managerUID,
			Controller: new(true),
		}},
	}}
}

func (*createRaceAdapter) SyncJob(ctx context.Context, _ client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) (bool, error) {
	remoteJob := &batchv1.Job{}
	err := remoteClient.Get(ctx, key, remoteJob)
	if client.IgnoreNotFound(err) != nil {
		return false, err
	}
	if err == nil {
		return false, nil
	}
	remoteJob = &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:      key.Name,
		Namespace: key.Namespace,
		Labels: map[string]string{
			kueue.MultiKueueOriginLabel:     origin,
			constants.PrebuiltWorkloadLabel: workloadName,
		},
	}}
	return false, client.IgnoreAlreadyExists(remoteClient.Create(ctx, remoteJob))
}

func (*createRaceAdapter) DeleteRemoteObject(context.Context, client.Client, client.Client, types.NamespacedName) error {
	return nil
}

func (*createRaceAdapter) IsJobManagedByKueue(context.Context, client.Client, types.NamespacedName) (bool, string, error) {
	return true, "", nil
}

func (*createRaceAdapter) GVK() schema.GroupVersionKind {
	return batchv1.SchemeGroupVersion.WithKind("Job")
}

func TestRemoteObjectOwnershipClientAuthenticatesCreateRace(t *testing.T) {
	const (
		origin       = "origin"
		workloadName = "workload"
		managerUID   = types.UID("manager-job-uid")
	)
	key := types.NamespacedName{Namespace: "ns", Name: "job"}
	managerJob := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, UID: managerUID}}

	tests := map[string]struct {
		winnerOriginUID types.UID
		wantErr         error
	}{
		"same manager object may win a concurrent multi-Workload create": {
			winnerOriginUID: managerUID,
		},
		"foreign object cannot hide behind ignored AlreadyExists": {
			winnerOriginUID: "foreign-manager-uid",
			wantErr:         ErrRemoteObjectNotOwnedByMultiKueue,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := batchv1.AddToScheme(scheme); err != nil {
				t.Fatalf("adding batch scheme: %v", err)
			}
			managerClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(managerJob.DeepCopy()).Build()
			baseWorkerClient := fake.NewClientBuilder().WithScheme(scheme).Build()
			winner := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
				Name:      key.Name,
				Namespace: key.Namespace,
				Labels: map[string]string{
					kueue.MultiKueueOriginLabel:     origin,
					constants.PrebuiltWorkloadLabel: workloadName,
				},
				Annotations: map[string]string{kueue.MultiKueueOriginUIDAnnotation: string(tc.winnerOriginUID)},
			}}
			workerClient := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
				Create: func(ctx context.Context, _ client.WithWatch, _ client.Object, _ ...client.CreateOption) error {
					if err := baseWorkerClient.Create(ctx, winner.DeepCopy()); err != nil {
						return err
					}
					return apierrors.NewAlreadyExists(schema.GroupResource{Group: batchv1.GroupName, Resource: "jobs"}, key.Name)
				},
				Get: func(ctx context.Context, _ client.WithWatch, getKey client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					return baseWorkerClient.Get(ctx, getKey, obj, opts...)
				},
			}).Build()

			ctx, _ := utiltesting.ContextWithLog(t)
			_, err := SyncJobWithRemoteObjectOwnership(ctx, managerClient, managerClient, workerClient, &createRaceAdapter{}, key, jobBoundWorkload(workloadName, key, managerUID), origin)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("SyncJobWithRemoteObjectOwnership() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestRemoteObjectOwnershipClientRejectsSameNameManagerReplacement(t *testing.T) {
	const (
		origin         = "origin"
		workloadName   = "workload"
		originalUID    = types.UID("original-manager-job-uid")
		replacementUID = types.UID("replacement-manager-job-uid")
	)
	key := types.NamespacedName{Namespace: "ns", Name: "job"}
	scheme := runtime.NewScheme()
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding batch scheme: %v", err)
	}
	managerOriginal := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name: key.Name, Namespace: key.Namespace, UID: originalUID,
	}}
	managerReplacement := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name: key.Name, Namespace: key.Namespace, UID: replacementUID,
	}}
	managerClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(managerOriginal).Build()
	managerReader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(managerReplacement).Build()
	workerClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	ctx, _ := utiltesting.ContextWithLog(t)
	_, err := SyncJobWithRemoteObjectOwnership(
		ctx,
		managerClient,
		managerReader,
		workerClient,
		&createRaceAdapter{},
		key,
		jobBoundWorkload(workloadName, key, originalUID),
		origin,
	)
	if !errors.Is(err, ErrRemoteObjectNotOwnedByMultiKueue) {
		t.Fatalf("SyncJobWithRemoteObjectOwnership() error = %v, want %v", err, ErrRemoteObjectNotOwnedByMultiKueue)
	}
	remoteJob := &batchv1.Job{}
	if err := workerClient.Get(ctx, key, remoteJob); !apierrors.IsNotFound(err) {
		t.Fatalf("worker Job Get() error = %v, want NotFound", err)
	}
}

func TestRemoteObjectOwnershipClientWriteBoundaries(t *testing.T) {
	const (
		origin       = "origin"
		workloadName = "workload"
		managerUID   = types.UID("manager-job-uid")
	)
	key := types.NamespacedName{Namespace: "ns", Name: "job"}
	scheme := runtime.NewScheme()
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding batch scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding core scheme: %v", err)
	}
	managerJob := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, UID: managerUID}}
	managerClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(managerJob).Build()
	workerClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&batchv1.Job{}).Build()
	guarded, err := newRemoteObjectOwnershipClientForSync(managerClient, managerClient, workerClient, &createRaceAdapter{}, key, jobBoundWorkload(workloadName, key, managerUID), origin)
	if err != nil {
		t.Fatalf("newRemoteObjectOwnershipClientForSync() error = %v", err)
	}
	ctx, _ := utiltesting.ContextWithLog(t)
	remoteJob := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:      key.Name,
		Namespace: key.Namespace,
		UID:       "remote-job-uid",
		Labels: map[string]string{
			kueue.MultiKueueOriginLabel:     origin,
			constants.PrebuiltWorkloadLabel: workloadName,
		},
	}}
	if err := guarded.Create(ctx, remoteJob); err != nil {
		t.Fatalf("guarded Create() error = %v", err)
	}
	if got := remoteJob.Annotations[kueue.MultiKueueOriginUIDAnnotation]; got != string(managerUID) {
		t.Fatalf("origin UID annotation = %q, want %q", got, managerUID)
	}
	configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "secondary", Namespace: key.Namespace}}
	if err := guarded.Get(ctx, client.ObjectKeyFromObject(configMap), configMap); !errors.Is(err, ErrRemoteObjectAccessUnsupported) {
		t.Fatalf("secondary-GVK Get() error = %v, want %v", err, ErrRemoteObjectAccessUnsupported)
	}
	if err := guarded.Create(ctx, configMap); !errors.Is(err, ErrRemoteObjectWriteUnsupported) {
		t.Fatalf("secondary-GVK Create() error = %v, want %v", err, ErrRemoteObjectWriteUnsupported)
	}

	if err := guarded.Get(ctx, key, remoteJob); err != nil {
		t.Fatalf("guarded Get() error = %v", err)
	}
	if err := clientutil.Patch(ctx, guarded, remoteJob, func() (bool, error) {
		remoteJob.Labels["patched"] = "true"
		return true, nil
	}); err != nil {
		t.Fatalf("strict guarded Patch() error = %v", err)
	}
	if err := guarded.Get(ctx, key, remoteJob); err != nil {
		t.Fatalf("guarded Get() after patch error = %v", err)
	}
	remoteJob.Labels["updated"] = "true"
	if err := guarded.Update(ctx, remoteJob); err != nil {
		t.Fatalf("guarded Update() error = %v", err)
	}
	if err := guarded.Get(ctx, key, remoteJob); err != nil {
		t.Fatalf("guarded Get() after update error = %v", err)
	}
	remoteJob.Status.Active = 1
	if err := guarded.Status().Update(ctx, remoteJob); err != nil {
		t.Fatalf("guarded Status().Update() error = %v", err)
	}
	if err := guarded.Get(ctx, key, remoteJob); err != nil {
		t.Fatalf("guarded Get() after status update error = %v", err)
	}
	if remoteJob.Status.Active != 1 {
		t.Fatalf("remote Job active status = %d, want 1", remoteJob.Status.Active)
	}
	loosePatch := client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"labels":{"loose":"true"}}}`))
	if err := guarded.Patch(ctx, remoteJob, loosePatch); !errors.Is(err, ErrRemoteObjectWriteUnsupported) {
		t.Fatalf("unguarded Patch() error = %v, want %v", err, ErrRemoteObjectWriteUnsupported)
	}
	if err := guarded.Status().Patch(ctx, remoteJob, loosePatch); !errors.Is(err, ErrRemoteObjectWriteUnsupported) {
		t.Fatalf("unguarded Status().Patch() error = %v, want %v", err, ErrRemoteObjectWriteUnsupported)
	}
	alternateBody := remoteJob.DeepCopy()
	alternateBody.Annotations[kueue.MultiKueueOriginUIDAnnotation] = "foreign-manager-uid"
	if err := guarded.Status().Update(ctx, remoteJob, client.WithSubResourceBody(alternateBody)); !errors.Is(err, ErrRemoteObjectWriteUnsupported) {
		t.Fatalf("Status().Update() with alternate body error = %v, want %v", err, ErrRemoteObjectWriteUnsupported)
	}
	patchBase := remoteJob.DeepCopy()
	remoteJob.Labels["strict-patch"] = "true"
	strictPatch := client.MergeFromWithOptions(patchBase, client.MergeFromWithOptimisticLock{})
	if err := guarded.Status().Patch(ctx, remoteJob, strictPatch, client.WithSubResourceBody(alternateBody)); !errors.Is(err, ErrRemoteObjectWriteUnsupported) {
		t.Fatalf("Status().Patch() with alternate body error = %v, want %v", err, ErrRemoteObjectWriteUnsupported)
	}
	if err := guarded.SubResource("scale").Get(ctx, remoteJob, &metav1.PartialObjectMetadata{}); !errors.Is(err, ErrRemoteObjectAccessUnsupported) {
		t.Fatalf("SubResource().Get() error = %v, want %v", err, ErrRemoteObjectAccessUnsupported)
	}
	if err := guarded.SubResource("scale").Create(ctx, remoteJob, &metav1.PartialObjectMetadata{}); !errors.Is(err, ErrRemoteObjectWriteUnsupported) {
		t.Fatalf("SubResource().Create() error = %v, want %v", err, ErrRemoteObjectWriteUnsupported)
	}
	if err := guarded.Apply(ctx, nil); !errors.Is(err, ErrRemoteObjectWriteUnsupported) {
		t.Fatalf("Apply() error = %v, want %v", err, ErrRemoteObjectWriteUnsupported)
	}
	if err := guarded.Status().Apply(ctx, nil); !errors.Is(err, ErrRemoteObjectWriteUnsupported) {
		t.Fatalf("Status().Apply() error = %v, want %v", err, ErrRemoteObjectWriteUnsupported)
	}
}

func TestRemoteObjectOwnershipClientFiltersListAndDeleteAllOf(t *testing.T) {
	const (
		origin       = "origin"
		workloadName = "workload"
	)
	scheme := runtime.NewScheme()
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding batch scheme: %v", err)
	}
	makeManagerJob := func(name string, uid types.UID) *batchv1.Job {
		return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", UID: uid}}
	}
	makeRemoteJob := func(name, workload string, managerUID types.UID) *batchv1.Job {
		return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "ns",
			UID:       types.UID("remote-" + name + "-uid"),
			Labels: map[string]string{
				kueue.MultiKueueOriginLabel:     origin,
				constants.PrebuiltWorkloadLabel: workload,
			},
			Annotations: map[string]string{kueue.MultiKueueOriginUIDAnnotation: string(managerUID)},
		}}
	}
	associatedManager := makeManagerJob("associated", "manager-associated-uid")
	otherWorkloadManager := makeManagerJob("other-workload", "manager-other-workload-uid")
	foreignManager := makeManagerJob("foreign", "manager-foreign-uid")
	associated := makeRemoteJob(associatedManager.Name, workloadName, associatedManager.UID)
	otherWorkload := makeRemoteJob(otherWorkloadManager.Name, "another-workload", otherWorkloadManager.UID)
	foreign := makeRemoteJob(foreignManager.Name, workloadName, "different-manager-uid")

	managerClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(associatedManager, otherWorkloadManager, foreignManager).Build()
	workerClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(associated, otherWorkload, foreign).Build()
	guarded, err := newRemoteObjectOwnershipClientForSync(
		managerClient,
		managerClient,
		workerClient,
		&createRaceAdapter{},
		client.ObjectKeyFromObject(associatedManager),
		jobBoundWorkload(workloadName, client.ObjectKeyFromObject(associatedManager), associatedManager.UID),
		origin,
	)
	if err != nil {
		t.Fatalf("newRemoteObjectOwnershipClientForSync() error = %v", err)
	}
	ctx, _ := utiltesting.ContextWithLog(t)
	jobs := &batchv1.JobList{}
	if err := guarded.List(ctx, jobs, client.InNamespace("ns")); err != nil {
		t.Fatalf("guarded List() error = %v", err)
	}
	if len(jobs.Items) != 1 || jobs.Items[0].Name != associated.Name {
		t.Fatalf("guarded List() jobs = %v, want only %q", jobNames(jobs.Items), associated.Name)
	}

	if err := guarded.DeleteAllOf(ctx, &batchv1.Job{}, client.InNamespace("ns")); err != nil {
		t.Fatalf("guarded DeleteAllOf() error = %v", err)
	}
	if err := workerClient.Get(ctx, client.ObjectKeyFromObject(associated), &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("associated Job Get() after DeleteAllOf error = %v, want NotFound", err)
	}
	for _, preserved := range []*batchv1.Job{otherWorkload, foreign} {
		if err := workerClient.Get(ctx, client.ObjectKeyFromObject(preserved), &batchv1.Job{}); err != nil {
			t.Fatalf("preserved Job %q Get() error = %v", preserved.Name, err)
		}
	}
}

func jobNames(jobs []batchv1.Job) []string {
	names := make([]string, len(jobs))
	for i := range jobs {
		names[i] = jobs[i].Name
	}
	return names
}
