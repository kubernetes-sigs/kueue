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

package workloadslicing

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/kubernetes/scheme"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestEnabled(t *testing.T) {
	type args struct {
		object metav1.Object
	}
	tests := map[string]struct {
		args args
		want bool
	}{
		"NilObject": {},
		"NilAnnotation": {
			args: args{
				object: &batchv1.Job{},
			},
		},
		"EmptyAnnotation": {
			args: args{
				object: &batchv1.Job{
					ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}},
				},
			},
		},
		"Enabled": {
			args: args{
				object: &batchv1.Job{
					ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
						EnabledAnnotationKey: EnabledAnnotationValue,
					}},
				},
			},
			want: true,
		},
		"NotEnabled": {
			args: args{
				object: &batchv1.Job{
					ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
						EnabledAnnotationKey: "True", // <-- value is case sensitive.
					}},
				},
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := Enabled(tt.args.object); got != tt.want {
				t.Errorf("Enabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPreemptibleSliceKey(t *testing.T) {
	type args struct {
		wl *kueue.Workload
	}
	tests := map[string]struct {
		args args
		want string
	}{
		"NilAnnotations": {
			args: args{
				wl: &kueue.Workload{},
			},
		},
		"EmptyAnnotations": {
			args: args{
				wl: &kueue.Workload{
					ObjectMeta: metav1.ObjectMeta{Annotations: make(map[string]string)},
				},
			},
		},
		"Found": {
			args: args{
				wl: &kueue.Workload{
					ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{WorkloadPreemptibleSliceNameKey: "test"}},
				},
			},
			want: "test",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := PreemptibleSliceKey(tt.args.wl); got != tt.want {
				t.Errorf("PreemptibleSliceKey() = %v, want %v", got, tt.want)
			}
		})
	}
}

var (
	testJobGVK = batchv1.SchemeGroupVersion.WithKind("Job")

	testJobObject = &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test",
			UID:  uuid.NewUUID(),
		},
	}
)

func testWorkload(name, ownerName string, created time.Time, active *bool) *kueue.Workload {
	return &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			CreationTimestamp: metav1.NewTime(created),
			Name:              name,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: testJobGVK.GroupVersion().String(),
					Kind:       testJobGVK.Kind,
					Name:       ownerName,
				},
			},
			ResourceVersion: "111",
		},
		Spec: kueue.WorkloadSpec{
			Active: active,
		},
	}
}

func testWorkloadClientBuilder() *fake.ClientBuilder {
	testSchema := runtime.NewScheme()
	_ = kueue.AddToScheme(testSchema)
	return fake.NewClientBuilder().
		WithScheme(testSchema).
		WithIndex(&kueue.Workload{}, fmt.Sprintf(".metadata.ownerReferences[%s.%s]", testJobGVK.Group, testJobGVK.Kind), func(object client.Object) []string {
			wl, ok := object.(*kueue.Workload)
			if !ok || len(wl.OwnerReferences) == 0 {
				return nil
			}
			owners := make([]string, 0, len(wl.OwnerReferences))
			for i := range wl.OwnerReferences {
				owner := &wl.OwnerReferences[i]
				if owner.Kind == testJobGVK.Kind && owner.APIVersion == testJobGVK.GroupVersion().String() {
					owners = append(owners, owner.Name)
				}
			}
			return owners
		})
}

func TestFindActiveSlices(t *testing.T) {
	type args struct {
		ctx          context.Context
		clnt         client.Client
		jobObject    client.Object
		jobObjectGVK schema.GroupVersionKind
	}

	// test "constants".
	now := time.Now()

	// test cases.
	tests := map[string]struct {
		args    args
		want    []kueue.Workload
		wantErr bool
	}{
		"ListFailure": {
			args: args{
				ctx:          t.Context(),
				clnt:         fake.NewFakeClient(),
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			wantErr: true,
		},
		"EmptyList": {
			args: args{
				ctx:          t.Context(),
				clnt:         testWorkloadClientBuilder().Build(),
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: []kueue.Workload{},
		},
		"SortedAndFiltered": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithLists(&kueue.WorkloadList{
					Items: []kueue.Workload{
						// Kept and sorted.
						*testWorkload("job-test-new", testJobObject.Name, now, nil),
						*testWorkload("job-test-old", testJobObject.Name, now.Add(-time.Minute), ptr.To(true)),
						// Excluded.
						*testWorkload("job-test-deactivated", testJobObject.Name, now, ptr.To(false)),
						*testWorkload("job-not-matching", "some-other-job", now, ptr.To(true)),
					},
				}).Build(),
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: []kueue.Workload{
				*testWorkload("job-test-old", testJobObject.Name, now.Add(-time.Minute), ptr.To(true)),
				*testWorkload("job-test-new", testJobObject.Name, now, nil),
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := FindActiveSlices(tt.args.ctx, tt.args.clnt, tt.args.jobObject, tt.args.jobObjectGVK)
			if (err != nil) != tt.wantErr {
				t.Errorf("FindActiveSlices() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if diff := cmp.Diff(got, tt.want, cmpopts.EquateApproxTime(time.Second)); diff != "" {
				t.Errorf("FindActiveSlices() got(-),want(+): %s", diff)
			}
		})
	}
}

func Test_deactivateOutOfSyncSlice(t *testing.T) {
	type args struct {
		ctx           context.Context
		clnt          client.Client
		workloadSlice *kueue.Workload
	}
	tests := map[string]struct {
		args    args
		wantErr bool
	}{
		"FailureToDeactivate": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
					Patch: func(_ context.Context, _ client.WithWatch, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
						return errors.New("test-patch-spec-error")
					},
				}).Build(),
				workloadSlice: &kueue.Workload{},
			},
			wantErr: true,
		},
		"FailureToFinish": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
					SubResourcePatch: func(_ context.Context, _ client.Client, _ string, _ client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
						return errors.New("test-patch-status-error")
					},
				}).Build(),
				workloadSlice: &kueue.Workload{
					Spec: kueue.WorkloadSpec{
						Active: ptr.To(false),
					},
				},
			},
			wantErr: true,
		},
		"DeactivatedAndFinished": {
			args: args{
				ctx: t.Context(),
				workloadSlice: &kueue.Workload{
					Spec: kueue.WorkloadSpec{
						Active: ptr.To(false),
					},
					Status: kueue.WorkloadStatus{
						Conditions: []metav1.Condition{
							{
								Type:    kueue.WorkloadFinished,
								Status:  metav1.ConditionTrue,
								Reason:  kueue.WorkloadFinishedReasonOutOfSync,
								Message: "The workload slice is out of sync with its parent job",
							},
						},
					},
				},
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if err := deactivateOutOfSyncSlice(tt.args.ctx, tt.args.clnt, tt.args.workloadSlice); (err != nil) != tt.wantErr {
				t.Errorf("deactivateOutOfSyncSlice() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestEnsureWorkloadSlices(t *testing.T) {
	type args struct {
		ctx          context.Context
		clnt         client.Client
		jobPodSets   []kueue.PodSet
		jobObject    client.Object
		jobObjectGVK schema.GroupVersionKind
	}
	type want struct {
		workload   *kueue.Workload
		compatible bool
		error      bool
	}
	fiveMinutesAgo := time.Now().Add(-5 * time.Minute)
	testPodSets := func(count int32) []kueue.PodSet {
		return []kueue.PodSet{
			{
				Name:  kueue.DefaultPodSetName,
				Count: count,
			},
		}
	}
	testResourceVersion := int64(100)
	// testWorkload helper constructs a workload object with the provided name, resourceVersion/generation and podSets.
	// The workload's creation time is offset by 5 minutes in the past + N milliseconds, where N is "derived"
	// from the resource version, to adjust for later creation time.
	// For example: resource version 100 - will result in creation timestamp 5 min ago + 100 millis.
	testWorkload := func(name string, resourceVersion int64, podSets []kueue.PodSet) *kueue.Workload {
		return &kueue.Workload{
			ObjectMeta: metav1.ObjectMeta{
				Name:              name,
				Namespace:         testJobObject.Namespace,
				ResourceVersion:   strconv.FormatInt(resourceVersion, 10),
				CreationTimestamp: metav1.NewTime(fiveMinutesAgo.Add(time.Duration(resourceVersion) * time.Millisecond)),
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: testJobGVK.GroupVersion().String(),
						Kind:       testJobGVK.Kind,
						Name:       testJobObject.Name,
					},
				},
			},
			Spec: kueue.WorkloadSpec{
				PodSets: podSets,
			},
		}
	}
	withQuotaReservationCondition := func(wl *kueue.Workload) *kueue.Workload {
		workload.SetQuotaReservation(wl, &kueue.Admission{}, clocktesting.NewFakeClock(time.Time{}))
		return wl
	}
	tests := map[string]struct {
		args args
		want want
	}{
		"FailedListWorkloads": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().
					WithInterceptorFuncs(interceptor.Funcs{
						List: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) error {
							return errors.New("test-list-error")
						},
					}).
					Build(),
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				error:      true,
				compatible: true,
			},
		},
		"NoWorkloadSlices": {
			args: args{
				ctx:          t.Context(),
				clnt:         testWorkloadClientBuilder().Build(),
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
			},
		},
		"OneWorkloadSlice_IncompatibleWithJob": {
			args: args{
				ctx:  t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(testWorkload(testJobObject.Name, testResourceVersion, testPodSets(1))).Build(),
				jobPodSets: []kueue.PodSet{
					{
						Name:  "DifferentKey",
						Count: 1,
					},
				},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
		},
		"OneWorkloadSlice_CurrentWorkload": {
			args: args{
				ctx:          t.Context(),
				clnt:         testWorkloadClientBuilder().WithObjects(testWorkload(testJobObject.Name, testResourceVersion, testPodSets(1))).Build(),
				jobPodSets:   testPodSets(1),
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				workload:   testWorkload(testJobObject.Name, testResourceVersion, testPodSets(1)),
				compatible: true,
			},
		},
		"OneWorkloadSlice_ScaledUpUnreserved": {
			args: args{
				ctx:          t.Context(),
				clnt:         testWorkloadClientBuilder().WithObjects(testWorkload(testJobObject.Name, testResourceVersion, testPodSets(1))).Build(),
				jobPodSets:   testPodSets(3),
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				workload:   testWorkload(testJobObject.Name, testResourceVersion+1, testPodSets(3)),
				compatible: true,
			},
		},
		"OneWorkloadSlice_ScaledUpReserved": {
			args: args{
				ctx:          t.Context(),
				clnt:         testWorkloadClientBuilder().WithObjects(withQuotaReservationCondition(testWorkload(testJobObject.Name, testResourceVersion, testPodSets(1)))).Build(),
				jobPodSets:   testPodSets(3),
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
			},
		},
		"OneWorkloadSlice_ScaledDown": {
			args: args{
				ctx:          t.Context(),
				clnt:         testWorkloadClientBuilder().WithObjects(testWorkload(testJobObject.Name, testResourceVersion, testPodSets(3))).Build(),
				jobPodSets:   testPodSets(1),
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				workload:   testWorkload(testJobObject.Name, testResourceVersion+1, testPodSets(1)),
				compatible: true,
			},
		},
		"OneWorkloadSlice_FailedToUpdate": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(testWorkload(testJobObject.Name, testResourceVersion, testPodSets(3))).
					WithInterceptorFuncs(interceptor.Funcs{
						Update: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.UpdateOption) error {
							return errors.New("test-update-error")
						},
					}).Build(),
				jobPodSets:   testPodSets(1),
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				error:      true,
				compatible: true,
			},
		},
		"TwoWorkloadSlices_IncompatibleJob": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					testWorkload(testJobObject.Name+"-1", 1, testPodSets(1)),
					testWorkload(testJobObject.Name+"-2", 1, testPodSets(1))).
					Build(),
				jobPodSets: []kueue.PodSet{
					{
						Name:  "DifferentKey",
						Count: 1,
					},
				},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
		},
		"TwoWorkloadSlices_NoChanges": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					testWorkload(testJobObject.Name+"-1", testResourceVersion, testPodSets(1)),
					withQuotaReservationCondition(testWorkload(testJobObject.Name+"-2", testResourceVersion+1, testPodSets(2)))).
					Build(),
				jobPodSets:   testPodSets(2),
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				workload:   withQuotaReservationCondition(testWorkload(testJobObject.Name+"-2", testResourceVersion+1, testPodSets(2))),
				compatible: true,
			},
		},
		"TwoWorkloadSlices_WithQuotaReservation": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					testWorkload(testJobObject.Name+"-1", testResourceVersion, testPodSets(1)),
					withQuotaReservationCondition(testWorkload(testJobObject.Name+"-2", testResourceVersion+1, testPodSets(2)))).
					Build(),
				jobPodSets:   testPodSets(2),
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				workload:   withQuotaReservationCondition(testWorkload(testJobObject.Name+"-2", testResourceVersion+1, testPodSets(2))),
				compatible: true,
			},
		},
		"TwoWorkloadSlices_OutOfSync": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					testWorkload(testJobObject.Name+"-1", testResourceVersion, testPodSets(1)),
					testWorkload(testJobObject.Name+"-2", testResourceVersion+1, testPodSets(2))).
					WithStatusSubresource(testWorkload(testJobObject.Name+"-2", testResourceVersion+1, testPodSets(2))).
					Build(),
				jobPodSets:   testPodSets(3),
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				workload: func() *kueue.Workload {
					wl := testWorkload(testJobObject.Name+"-2", testResourceVersion+3, testPodSets(2))
					wl.Spec.Active = ptr.To(false)
					apimeta.SetStatusCondition(&wl.Status.Conditions, finishedOutOfSyncCondition())
					return wl
				}(),
				compatible: true,
			},
		},
		"MoreThanTwoWorkloadSlices": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					testWorkload(testJobObject.Name+"-1", testResourceVersion, testPodSets(1)),
					testWorkload(testJobObject.Name+"-2", testResourceVersion+1, testPodSets(2)),
					testWorkload(testJobObject.Name+"-3", testResourceVersion+1, testPodSets(3))).
					Build(),
				jobPodSets:   testPodSets(3),
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				error:      true,
				compatible: true,
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			gotWorkload, gotCompatible, gotError := EnsureWorkloadSlices(tt.args.ctx, tt.args.clnt, tt.args.jobPodSets, tt.args.jobObject, tt.args.jobObjectGVK)
			if (gotError != nil) != tt.want.error {
				t.Errorf("EnsureWorkloadSlices() error = %v, wantErr %v", gotError, tt.want.error)
				return
			}
			if diff := cmp.Diff(gotWorkload, tt.want.workload, cmpopts.EquateApproxTime(time.Second)); diff != "" {
				t.Errorf("EnsureWorkloadSlices() got(-),want(+): %s", diff)
			}
			if gotCompatible != tt.want.compatible {
				t.Errorf("EnsureWorkloadSlices() got1 = %v, want %v", gotCompatible, tt.want.compatible)
			}
		})
	}
}

func Test_startWorkloadSlicePods(t *testing.T) {
	clientBuilder := func() *fake.ClientBuilder {
		return fake.NewClientBuilder().WithScheme(scheme.Scheme).
			WithIndex(&corev1.Pod{}, indexer.OwnerReferenceUID, indexer.IndexOwnerUID)
	}
	testPod := func(name, resourceVersion string, owner client.Object, schedulingGates ...corev1.PodSchedulingGate) corev1.Pod {
		return corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: owner.GetObjectKind().GroupVersionKind().GroupVersion().String(),
						Kind:       owner.GetObjectKind().GroupVersionKind().Kind,
						Name:       owner.GetName(),
						UID:        owner.GetUID(),
					},
				},
				ResourceVersion: resourceVersion,
			},
			Spec: corev1.PodSpec{
				SchedulingGates: schedulingGates,
			},
		}
	}

	type args struct {
		ctx    context.Context
		clnt   client.Client
		object client.Object
	}
	tests := map[string]struct {
		args     args
		wantErr  bool
		wantPods *corev1.PodList
	}{
		"FailureToListPods": {
			args: args{
				ctx: t.Context(),
				clnt: clientBuilder().WithInterceptorFuncs(interceptor.Funcs{
					List: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) error {
						return errors.New("test-list-pods-error")
					},
				}).Build(),
				object: testJobObject,
			},
			wantErr: true,
		},
		"NoPods": {
			args: args{
				ctx:    t.Context(),
				clnt:   clientBuilder().Build(),
				object: testJobObject,
			},
		},
		"ProcessPods": {
			args: args{
				ctx: t.Context(),
				clnt: clientBuilder().WithLists(&corev1.PodList{
					Items: []corev1.Pod{
						// Un-gated pod should remain un-gated, i.e., no change.
						testPod("test-one", "100", testJobObject),
						// Gated pod - gate should be removed.
						testPod("test-two", "200", testJobObject, corev1.PodSchedulingGate{Name: kueue.WorkloadSliceSchedulingGate}),
						// Gated with some other gate -
						testPod("test-three", "300", testJobObject, corev1.PodSchedulingGate{Name: kueue.WorkloadSliceSchedulingGate}, corev1.PodSchedulingGate{Name: kueuealpha.TopologySchedulingGate}),
						// Other gated pod (not for this job)
						testPod("other-pod", "400", &batchv1.Job{
							ObjectMeta: metav1.ObjectMeta{
								Name: "other-job",
							},
						}, corev1.PodSchedulingGate{Name: kueue.WorkloadSliceSchedulingGate}),
					},
				}).Build(),
				object: testJobObject,
			},
			wantPods: &corev1.PodList{
				Items: []corev1.Pod{
					testPod("test-one", "100", testJobObject),
					// Gated pod - gate removed (resource version increase).
					testPod("test-two", "201", testJobObject),
					// Gated with some other gate - other gate remains (resource version increased).
					testPod("test-three", "301", testJobObject, corev1.PodSchedulingGate{Name: kueuealpha.TopologySchedulingGate}),
					// Other gated pod (not for this job) - no change.
					testPod("other-pod", "400", &batchv1.Job{
						ObjectMeta: metav1.ObjectMeta{
							Name: "other-job",
						},
					}, corev1.PodSchedulingGate{Name: kueue.WorkloadSliceSchedulingGate}),
				},
			},
		},
		"FailureUpdatingPod": {
			args: args{
				ctx: t.Context(),
				clnt: clientBuilder().WithLists(&corev1.PodList{
					Items: []corev1.Pod{
						testPod("test", "100", testJobObject, corev1.PodSchedulingGate{Name: kueue.WorkloadSliceSchedulingGate}),
					},
				}).WithInterceptorFuncs(interceptor.Funcs{
					Patch: func(_ context.Context, _ client.WithWatch, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
						return errors.New("test-pod-patch-error")
					},
				}).
					Build(),
				object: testJobObject,
			},
			wantErr: true,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if err := startWorkloadSlicePods(tt.args.ctx, tt.args.clnt, tt.args.object); (err != nil) != tt.wantErr {
				t.Errorf("startWorkloadSlicePods() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantPods == nil {
				return
			}

			pods := &corev1.PodList{}
			if err := tt.args.clnt.List(tt.args.ctx, pods); err != nil {
				t.Errorf("unexpected list error: %v", err)
			}
			if diff := cmp.Diff(pods.Items, tt.wantPods.Items, cmpopts.SortSlices(func(a, b corev1.Pod) bool {
				return a.Name < b.Name
			})); diff != "" {
				t.Errorf("startWorkloadSlicePods() pod-validaion: got(-),want(+): %s", diff)
			}
		})
	}
}
