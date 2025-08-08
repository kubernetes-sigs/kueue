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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/hierarchy"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
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
	testReference := workload.NewReference("test", "test")
	tests := map[string]struct {
		args args
		want *workload.Reference
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
					ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{WorkloadSliceReplacementFor: string(testReference)}},
				},
			},
			want: &testReference,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(ReplacementForKey(tt.args.wl), tt.want); diff != "" {
				t.Errorf("ReplacementForKey() (-want,+got)\n:%s", diff)
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

func testWorkloadClientBuilder() *fake.ClientBuilder {
	testSchema := runtime.NewScheme()
	_ = kueue.AddToScheme(testSchema)
	return fake.NewClientBuilder().
		WithScheme(testSchema).
		WithIndex(&kueue.Workload{}, indexer.OwnerReferenceIndexKey(testJobGVK), indexer.WorkloadOwnerIndexFunc(testJobGVK))
}

func testWorkload(name, jobName string, jobUID types.UID, created time.Time) *utiltesting.WorkloadWrapper {
	return utiltesting.MakeWorkload(name, "default").
		OwnerReference(testJobGVK, jobName, string(jobUID)).
		Creation(created).
		ResourceVersion("1").
		Request(corev1.ResourceCPU, "100m")
}

func TestFindNotFinishedWorkloads(t *testing.T) {
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
						*testWorkload("test-2", testJobObject.Name, testJobObject.UID, now).ResourceVersion("200").Obj(),
						*testWorkload("test-1", testJobObject.Name, testJobObject.UID, now.Add(-time.Minute)).ResourceVersion("100").Obj(),
						*testWorkload("test-0", testJobObject.Name, testJobObject.UID, now.Add(-time.Hour)).ResourceVersion("10").Finished().Obj(),
						*testWorkload("test-4", "some-other-job", uuid.NewUUID(), now).ResourceVersion("100").Obj(),
					},
				}).Build(),
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: []kueue.Workload{
				*testWorkload("test-1", testJobObject.Name, testJobObject.UID, now.Add(-time.Minute)).ResourceVersion("100").Obj(),
				*testWorkload("test-2", testJobObject.Name, testJobObject.UID, now).ResourceVersion("200").Obj(),
			},
		},
		"TwoActiveWorkloads_WithoutTimestampCollision": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithLists(&kueue.WorkloadList{
					// Note: the workloads names and order is deliberate to assert that workloads are sorted
					// by creating timestamp and then (on collision) by the tiebreaker.
					//
					// Also note: we are deliberately using identical resourceVersion value to emphasize that
					// resourceVersion comes into play only with creationTimestamp collision.
					Items: []kueue.Workload{
						*testWorkload("test-22", testJobObject.Name, testJobObject.UID, now).
							ResourceVersion("200").
							Annotation(WorkloadSliceReplacementFor, string(workload.NewReference("default", "test-21"))).
							Obj(),
						*testWorkload("test-21", testJobObject.Name, testJobObject.UID, now.Add(-time.Second)).
							ResourceVersion("100").
							Obj(),
					},
				}).Build(),
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: []kueue.Workload{
				*testWorkload("test-21", testJobObject.Name, testJobObject.UID, now.Add(-time.Second)).
					ResourceVersion("100").
					Obj(),
				*testWorkload("test-22", testJobObject.Name, testJobObject.UID, now).
					ResourceVersion("200").
					Annotation(WorkloadSliceReplacementFor, string(workload.NewReference("default", "test-21"))).
					Obj(),
			},
		},
		"TwoActiveWorkloads_TimestampCollision": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithLists(&kueue.WorkloadList{
					// Note: the workloads names and order is deliberate to assert that workloads are sorted
					// by creating timestamp and then (on collision) by the tiebreaker.
					Items: []kueue.Workload{
						*testWorkload("test-22", testJobObject.Name, testJobObject.UID, now).
							ResourceVersion("200").
							Annotation(WorkloadSliceReplacementFor, string(workload.NewReference("default", "test-21"))).
							Obj(),
						*testWorkload("test-21", testJobObject.Name, testJobObject.UID, now).
							ResourceVersion("100").
							Annotation(WorkloadSliceReplacementFor, string(workload.NewReference("default", "test-20"))).
							Obj(),
					},
				}).Build(),
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: []kueue.Workload{
				*testWorkload("test-21", testJobObject.Name, testJobObject.UID, now).
					ResourceVersion("100").
					Annotation(WorkloadSliceReplacementFor, string(workload.NewReference("default", "test-20"))).
					Obj(),
				*testWorkload("test-22", testJobObject.Name, testJobObject.UID, now).
					ResourceVersion("200").
					Annotation(WorkloadSliceReplacementFor, string(workload.NewReference("default", "test-21"))).
					Obj(),
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := FindNotFinishedWorkloads(tt.args.ctx, tt.args.clnt, tt.args.jobObject, tt.args.jobObjectGVK)
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

func TestFinish(t *testing.T) {
	type args struct {
		ctx           context.Context
		clnt          client.Client
		workloadSlice *kueue.Workload
		reason        string
		message       string
	}
	type want struct {
		err bool
		// side effect.
		workload *kueue.Workload
	}
	now := time.Now()
	tests := map[string]struct {
		args args
		want want
	}{
		"FailureToApplyConditions": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().
					WithInterceptorFuncs(interceptor.Funcs{
						SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
							return errors.New("test-patch-status-error")
						},
					}).
					Build(),
				workloadSlice: testWorkload("test", "test-job", "job-uid", now).Obj(),
				reason:        "TestReason",
				message:       "Test Message.",
			},
			want: want{err: true},
		},
		"AlreadyFinishedWorkload": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().
					WithObjects(testWorkload("test", "test-job", "job-uid", now).Finished().Obj()).Build(),
				workloadSlice: testWorkload("test", "test-job", "job-uid", now).Finished().Obj(),
				reason:        "TestReason",
				message:       "Test Message.",
			},
			want: want{
				workload: testWorkload("test", "test-job", "job-uid", now).Finished().Obj(),
			},
		},
		"NotFinished": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().
					WithObjects(testWorkload("test", "test-job", "job-uid", now).Obj()).
					WithStatusSubresource(testWorkload("test", "test-job", "job-uid", now).Obj()).Build(),
				workloadSlice: testWorkload("test", "test-job", "job-uid", now).Obj(),
				reason:        "TestReason",
				message:       "Test Message.",
			},
			want: want{
				workload: testWorkload("test", "test-job", "job-uid", now).
					ResourceVersion("2").
					Condition(metav1.Condition{
						Type:    kueue.WorkloadFinished,
						Status:  metav1.ConditionTrue,
						Reason:  "TestReason",
						Message: "Test Message.",
					}).Obj(),
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if err := Finish(tt.args.ctx, tt.args.clnt, tt.args.workloadSlice, tt.args.reason, tt.args.message); (err != nil) != tt.want.err {
				t.Errorf("Finish() error = %v, wantErr %v", err, tt.want.err)
			}
			if tt.want.workload != nil {
				if err := tt.args.clnt.Get(tt.args.ctx, client.ObjectKeyFromObject(tt.args.workloadSlice), tt.args.workloadSlice); err != nil {
					t.Errorf("unexpected error retrieving workload: %v", err)
				}
				if diff := cmp.Diff(tt.want.workload, tt.args.workloadSlice, cmpopts.SortSlices(func(a, b metav1.Condition) bool {
					return a.Type < b.Type
				}), cmpopts.EquateApproxTime(time.Second)); diff != "" {
					t.Errorf("Deactivated() (-want,+got):\n%s", diff)
				}
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
	now := time.Now()
	fiveMinutesAgo := now.Add(-5 * time.Minute)
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

	assertStatusConditionPatch := func(t *testing.T, subResourceName string, obj client.Object, wantWorkloadName string, activeConditionType, activeConditionReason string) error {
		// Assert side effect: old slice is aggregated and marked as "finished".
		if subResourceName != "status" {
			t.Errorf("unexpected workload patch subresource: %s", subResourceName)
		}
		wl, ok := obj.(*kueue.Workload)
		if !ok {
			t.Errorf("unexpected workload patch object type: %T", obj)
		}
		if wl.Name != wantWorkloadName {
			t.Errorf("unexpected workload name: %s", wl.Name)
		}
		condition := apimeta.FindStatusCondition(wl.Status.Conditions, activeConditionType)
		if condition == nil {
			t.Fatalf("patched condition: %s is not found", activeConditionType)
		}
		if condition.Status != metav1.ConditionTrue {
			t.Errorf("patched condition: %s is not active", activeConditionType)
		}
		if condition.Reason != activeConditionReason {
			t.Errorf("patched condition: %s reseason - want: %s, got: %s", activeConditionType, activeConditionReason, condition.Reason)
		}
		return nil
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
		// No workloads.
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
		// One workload.
		"OneWorkloadSlice_IncompatibleWithJob": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).Build(),
				jobPodSets:   []kueue.PodSet{*utiltesting.MakePodSet("different-name", 1).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
		},
		"OneWorkloadSlice_CurrentWorkload": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).Build(),
				jobPodSets:   []kueue.PodSet{*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				workload: utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("1").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
				compatible: true,
			},
		},
		"OneWorkloadSlice_ReservedQuota_ScaleUp": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuota(utiltesting.MakeAdmission("default", kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").AssignmentPodCount(1).Obj()).
						Obj()).Build(),
				jobPodSets:   []kueue.PodSet{*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
			},
		},
		"OneWorkloadSlice_ReservedQuota_ScaleUp_MultiplePodSets": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						PodSets(
							*utiltesting.MakePodSet("scale-up", 3).Request(corev1.ResourceCPU, "1").Obj(),
							*utiltesting.MakePodSet("scale-down", 3).Request(corev1.ResourceCPU, "1").Obj(),
							*utiltesting.MakePodSet("stay-the-same", 3).Request(corev1.ResourceCPU, "1").Obj(),
						).
						ReserveQuota(utiltesting.MakeAdmission("default", "scaled-up", "scale-down", "stay-the-same").
							AssignmentWithIndex(0, corev1.ResourceCPU, "default", "1").AssignmentPodCountWithIndex(0, 3).
							AssignmentWithIndex(1, corev1.ResourceCPU, "default", "1").AssignmentPodCountWithIndex(1, 3).
							AssignmentWithIndex(1, corev1.ResourceCPU, "default", "1").AssignmentPodCountWithIndex(1, 3).
							Obj()).
						Obj()).Build(),
				jobPodSets: []kueue.PodSet{
					*utiltesting.MakePodSet("scale-up", 4).Request(corev1.ResourceCPU, "1").Obj(),      // <-- scaled-up.
					*utiltesting.MakePodSet("stay-the-same", 3).Request(corev1.ResourceCPU, "1").Obj(), // <-- stayed the same.
					*utiltesting.MakePodSet("scale-down", 1).Request(corev1.ResourceCPU, "1").Obj(),    // <-- scaled-down.
				},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
			},
		},
		"OneWorkloadSlice_ReservedQuota_ScaleDown": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuota(utiltesting.MakeAdmission("default", kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").AssignmentPodCount(3).Obj()).
						Obj()).Build(),
				jobPodSets:   []kueue.PodSet{*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				workload: utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("2").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("default", kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").AssignmentPodCount(3).Obj()).
					Obj(),
			},
		},
		"OneWorkloadSlice_ReservedQuota_ScaleDown_MultiplePodSets": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						PodSets(
							*utiltesting.MakePodSet("scale-down", 3).Request(corev1.ResourceCPU, "1").Obj(),
							*utiltesting.MakePodSet("stay-the-same", 3).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuota(utiltesting.MakeAdmission("default", "scale-down", "stay-the-same").
							AssignmentWithIndex(0, corev1.ResourceCPU, "default", "1").AssignmentPodCountWithIndex(0, 3).
							AssignmentWithIndex(1, corev1.ResourceCPU, "default", "1").AssignmentPodCountWithIndex(1, 3).
							Obj()).
						Obj()).Build(),
				jobPodSets: []kueue.PodSet{
					*utiltesting.MakePodSet("scale-down", 1).Request(corev1.ResourceCPU, "1").Obj(),    // <-- scaled-down.
					*utiltesting.MakePodSet("stay-the-same", 3).Request(corev1.ResourceCPU, "1").Obj(), // <-- stayed the same.
				},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				workload: utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("2").
					PodSets(
						*utiltesting.MakePodSet("scale-down", 1).Request(corev1.ResourceCPU, "1").Obj(),
						*utiltesting.MakePodSet("stay-the-same", 3).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("default", "scale-down", "stay-the-same").
						AssignmentWithIndex(0, corev1.ResourceCPU, "default", "1").AssignmentPodCountWithIndex(0, 3).
						AssignmentWithIndex(1, corev1.ResourceCPU, "default", "1").AssignmentPodCountWithIndex(1, 3).
						Obj()).
					Obj(),
			},
		},
		"OneWorkloadSlice_UnreservedQuota_ScaleUp": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).Build(),
				jobPodSets:   []kueue.PodSet{*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				workload: utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("2").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
		},
		"OneWorkloadSlice_UnreservedQuota_ScaleDown": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).Build(),
				jobPodSets:   []kueue.PodSet{*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				workload: utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("2").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
		},
		"OneWorkloadSlice_UpdateFailure": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).WithInterceptorFuncs(interceptor.Funcs{
					Update: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						return errors.New("test-update-error")
					}}).Build(),
				jobPodSets:   []kueue.PodSet{*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				error:      true,
				compatible: true,
			},
		},
		//
		"TwoWorkloads_BothUnreserved_NewIsCurrent": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(fiveMinutesAgo).
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						Obj(),
					utiltesting.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(now).
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).
					WithInterceptorFuncs(interceptor.Funcs{
						SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
							return assertStatusConditionPatch(t, subResourceName, obj, testJobObject.Name+"-1", kueue.WorkloadFinished, kueue.WorkloadFinishedReasonOutOfSync)
						},
					}).
					Build(),
				jobPodSets:   []kueue.PodSet{*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				workload: utiltesting.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("1").
					Creation(now).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
		},
		"TwoWorkloads_BothUnreserved_NewIsCurrent_FailureToPatchOldSliceStatus": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(fiveMinutesAgo).
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						Obj(),
					utiltesting.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(now).
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).
					WithInterceptorFuncs(interceptor.Funcs{
						SubResourcePatch: func(_ context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
							return errors.New("test-patch-failure")
						},
					}).
					Build(),
				jobPodSets:   []kueue.PodSet{*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				error:      true,
				compatible: true,
			},
		},
		"TwoWorkloads_NewIsIncompatible": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(fiveMinutesAgo).
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuota(utiltesting.MakeAdmission("default", kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").AssignmentPodCount(1).Obj()).
						Obj(),
					utiltesting.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(now).
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).
					Build(),
				jobPodSets:   []kueue.PodSet{*utiltesting.MakePodSet("different-key", 1).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
		},
		"TwoWorkloads_BothWithReservedQuota": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(fiveMinutesAgo).
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuota(utiltesting.MakeAdmission("default", kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").AssignmentPodCount(1).Obj()).
						Obj(),
					utiltesting.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(now).
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuota(utiltesting.MakeAdmission("default", kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").AssignmentPodCount(3).Obj()).
						Obj()).
					Build(),
				jobPodSets:   []kueue.PodSet{*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				error:      true,
				compatible: true,
			},
		},
		"TwoWorkloads_OldWithReservedQuotaAndEvicted_NewWithoutQuotaReservation": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(fiveMinutesAgo).
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuota(utiltesting.MakeAdmission("default", kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").AssignmentPodCount(1).Obj()).
						Evicted().
						Obj(),
					utiltesting.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(now).
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).
					Build(),
				jobPodSets:   []kueue.PodSet{*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				error:      true,
				compatible: true,
			},
		},
		"TwoWorkloadSlices_NewIsUnreservedAndCurrent": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(fiveMinutesAgo).
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuota(utiltesting.MakeAdmission("default", kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").AssignmentPodCount(1).Obj()).
						Obj(),
					utiltesting.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(now).
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).Build(),
				jobPodSets:   []kueue.PodSet{*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				workload: utiltesting.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("1").
					Creation(now).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
		},
		"TwoWorkloadSlices_NewIsUnreservedAndOutOfSync_ScaleUp": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(fiveMinutesAgo).
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuota(utiltesting.MakeAdmission("default", kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").AssignmentPodCount(1).Obj()).
						Obj(),
					utiltesting.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(now).
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).
					Build(),
				jobPodSets:   []kueue.PodSet{*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				workload: utiltesting.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("2").
					Creation(now).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
		},
		"TwoWorkloadSlices_NewIsUnreservedAndOutOfSync_ScaleDown": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(fiveMinutesAgo).
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuota(utiltesting.MakeAdmission("default", kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").AssignmentPodCount(1).Obj()).
						Obj(),
					utiltesting.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(now).
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).
					Build(),
				jobPodSets:   []kueue.PodSet{*utiltesting.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				workload: utiltesting.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("2").
					Creation(now).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
		},
		"TwoWorkloadSlices_NewIsUnreservedAndOutOfSync_UpdateFailure": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltesting.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(fiveMinutesAgo).
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuota(utiltesting.MakeAdmission("default", kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").AssignmentPodCount(1).Obj()).
						Obj(),
					utiltesting.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(now).
						PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).
					WithInterceptorFuncs(interceptor.Funcs{
						Update: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.UpdateOption) error {
							// Assert that we are updating correct workload slice.
							if obj.GetName() != testJobObject.Name+"-2" {
								t.Errorf("unexptected workload update: %v", obj)
							}
							return errors.New("test-update-error")
						},
					}).
					Build(),
				jobPodSets:   []kueue.PodSet{*utiltesting.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				error:      true,
				compatible: true,
			},
		},
		//
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
			if diff := cmp.Diff(tt.want.workload, gotWorkload, cmpopts.EquateApproxTime(time.Second)); diff != "" {
				t.Errorf("EnsureWorkloadSlices() (-want,+got):\n%s", diff)
			}
			if gotCompatible != tt.want.compatible {
				t.Errorf("EnsureWorkloadSlices() compatible = %v, want %v", gotCompatible, tt.want.compatible)
			}
		})
	}
}

func Test_StartWorkloadSlicePods(t *testing.T) {
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
						testPod("test-two", "200", testJobObject, corev1.PodSchedulingGate{Name: kueue.ElasticJobSchedulingGate}),
						// Gated with some other gate -
						testPod("test-three", "300", testJobObject, corev1.PodSchedulingGate{Name: kueue.ElasticJobSchedulingGate}, corev1.PodSchedulingGate{Name: kueuealpha.TopologySchedulingGate}),
						// Other gated pod (not for this job)
						testPod("other-pod", "400", &batchv1.Job{
							ObjectMeta: metav1.ObjectMeta{
								Name: "other-job",
							},
						}, corev1.PodSchedulingGate{Name: kueue.ElasticJobSchedulingGate}),
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
					}, corev1.PodSchedulingGate{Name: kueue.ElasticJobSchedulingGate}),
				},
			},
		},
		"FailureUpdatingPod": {
			args: args{
				ctx: t.Context(),
				clnt: clientBuilder().WithLists(&corev1.PodList{
					Items: []corev1.Pod{
						testPod("test", "100", testJobObject, corev1.PodSchedulingGate{Name: kueue.ElasticJobSchedulingGate}),
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
			if err := StartWorkloadSlicePods(tt.args.ctx, tt.args.clnt, tt.args.object); (err != nil) != tt.wantErr {
				t.Errorf("StartWorkloadSlicePods() error = %v, wantErr %v", err, tt.wantErr)
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
				t.Errorf("StartWorkloadSlicePods() pod-validaion: got(-),want(+): %s", diff)
			}
		})
	}
}

func TestReplacedWorkloadSlice(t *testing.T) {
	type args struct {
		wl   *workload.Info
		snap *cache.Snapshot
	}
	type want struct {
		wl      *workload.Info
		targets []*preemption.Target
	}

	tests := map[string]struct {
		featureEnabled bool
		args           args
		want           want
	}{
		"FeatureNotEnabled": {},
		"EdgeCase_WorkloadIsNil": {
			featureEnabled: true,
		},
		"EdgeCase_SnapshotIsNil": {
			featureEnabled: true,
			args: args{
				wl: workload.NewInfo(utiltesting.MakeWorkload("test", "default").Obj()),
			},
		},
		"WorkloadWithoutReplacementAnnotation": {
			featureEnabled: true,
			args: args{
				wl:   workload.NewInfo(utiltesting.MakeWorkload("test", "default").Obj()),
				snap: &cache.Snapshot{},
			},
		},
		"ReplacedWorkloadIsNotFound_MissingClusterQueue": {
			featureEnabled: true,
			args: args{
				wl: workload.NewInfo(utiltesting.MakeWorkload("test-new", "default").
					Annotation(WorkloadSliceReplacementFor, "test-old").
					Obj()),
				snap: &cache.Snapshot{
					Manager: hierarchy.NewManagerForTest(
						map[kueue.CohortReference]*cache.CohortSnapshot{},
						map[kueue.ClusterQueueReference]*cache.ClusterQueueSnapshot{}),
				},
			},
		},
		"EdgeCase_ReplacedWorkloadIsNotFound_NotInClusterQueue": {
			featureEnabled: true,
			args: args{
				wl: workload.NewInfo(utiltesting.MakeWorkload("test-new", "default").
					Annotation(WorkloadSliceReplacementFor, "test-old").
					Admission(utiltesting.MakeAdmission("default").Obj()).
					Obj()),
				snap: &cache.Snapshot{
					Manager: hierarchy.NewManagerForTest(
						map[kueue.CohortReference]*cache.CohortSnapshot{},
						map[kueue.ClusterQueueReference]*cache.ClusterQueueSnapshot{
							"default": {},
						}),
				},
			},
		},
		"ReplacedWorkloadIsFound": {
			featureEnabled: true,
			args: args{
				wl: workload.NewInfo(utiltesting.MakeWorkload("test-new", "default").
					Annotation(WorkloadSliceReplacementFor, "test-old").
					Admission(utiltesting.MakeAdmission("default").Obj()).
					Obj()),
				snap: &cache.Snapshot{
					Manager: hierarchy.NewManagerForTest(
						map[kueue.CohortReference]*cache.CohortSnapshot{},
						map[kueue.ClusterQueueReference]*cache.ClusterQueueSnapshot{
							"default": {
								Workloads: map[workload.Reference]*workload.Info{
									"test-old": workload.NewInfo(utiltesting.MakeWorkload("test-old", "default").Obj()),
								},
							},
						}),
				},
			},
			want: want{
				wl: workload.NewInfo(utiltesting.MakeWorkload("test-old", "default").Obj()),
				targets: []*preemption.Target{
					{WorkloadInfo: workload.NewInfo(utiltesting.MakeWorkload("test-old", "default").Obj())},
				},
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, tt.featureEnabled)
			targets, wl := ReplacedWorkloadSlice(tt.args.wl, tt.args.snap)
			if diff := cmp.Diff(tt.want.targets, targets); diff != "" {
				t.Errorf("ReplacedWorkloadSlice() targets (+want,-got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.want.wl, wl); diff != "" {
				t.Errorf("ReplacedWorkloadSlice() workload (+want,-got):\n%s", diff)
			}
		})
	}
}

func TestScaledDown(t *testing.T) {
	type args struct {
		oldCounts workload.PodSetsCounts
		newCounts workload.PodSetsCounts
	}
	tests := map[string]struct {
		args args
		want bool
	}{
		"EmptyCounts": {},
		"OnePodSetScaledDown": {
			args: args{
				oldCounts: workload.PodSetsCounts{
					"foo": 3,
					"bar": 5,
				},
				newCounts: workload.PodSetsCounts{
					"foo": 3,
					"bar": 4,
				},
			},
			want: true,
		},
		"AllPodSetsScaledDown": {
			args: args{
				oldCounts: workload.PodSetsCounts{
					"foo": 3,
					"bar": 5,
				},
				newCounts: workload.PodSetsCounts{
					"foo": 2,
					"bar": 4,
				},
			},
			want: true,
		},
		"OnePodSetScaledDownAndOnePodSetScaledUp": {
			args: args{
				oldCounts: workload.PodSetsCounts{
					"foo": 3,
					"bar": 5,
				},
				newCounts: workload.PodSetsCounts{
					"foo": 2,
					"bar": 6,
				},
			},
		},
		// Edge cases.
		"ExtraneousPodSetScaledUp": {
			args: args{
				oldCounts: workload.PodSetsCounts{
					"foo": 3,
					"bar": 5,
				},
				newCounts: workload.PodSetsCounts{
					"foo": 2,
					"baz": 6, // <-- extraneous
				},
			},
			want: true,
		},
		"ExtraneousPodSetScaledDown": {
			args: args{
				oldCounts: workload.PodSetsCounts{
					"foo": 3,
					"bar": 5,
				},
				newCounts: workload.PodSetsCounts{
					"foo": 2,
					"baz": 1, // <-- extraneous
				},
			},
			want: true,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := ScaledDown(tt.args.oldCounts, tt.args.newCounts); got != tt.want {
				t.Errorf("ScaledDown() = %v, want %v", got, tt.want)
			}
		})
	}
}
