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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
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

func TestDeactivate(t *testing.T) {
	type args struct {
		ctx           context.Context
		clnt          client.Client
		workloadSlice *kueue.Workload
		conditions    []metav1.Condition
	}
	type want struct {
		err bool
		// side effect.
		workload *kueue.Workload
	}
	testCondition := func(conditionType string, status metav1.ConditionStatus, reason, message string, transitionTime metav1.Time) metav1.Condition {
		return metav1.Condition{
			Type:               conditionType,
			Status:             status,
			Reason:             reason,
			Message:            message,
			LastTransitionTime: transitionTime,
		}
	}
	now := metav1.Now()
	tests := map[string]struct {
		args args
		want want
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
			want: want{err: true},
		},
		"FailureToApplyConditions": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().
					WithObjects(testWorkload("test-wl", "test-job", time.Now(), ptr.To(true))).
					WithInterceptorFuncs(interceptor.Funcs{
						SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
							return errors.New("test-patch-status-error")
						},
					}).
					Build(),
				workloadSlice: testWorkload("test-wl", "test-job", time.Now(), ptr.To(true)),
				conditions: []metav1.Condition{
					testCondition("testOne", metav1.ConditionTrue, "test-reason", "Test message.", now),
				},
			},
			want: want{err: true},
		},
		"Active": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().
					WithObjects(func() *kueue.Workload {
						wl := testWorkload("test-wl", "test-job", time.Now(), ptr.To(true))
						wl.Status.Conditions = []metav1.Condition{
							testCondition("updated", metav1.ConditionFalse, "test-reason", "Test message.", now),
						}
						return wl
					}()).
					WithStatusSubresource(testWorkload("test-wl", "test-job", time.Now(), ptr.To(true))).
					Build(),
				workloadSlice: testWorkload("test-wl", "test-job", time.Now(), ptr.To(true)),
				conditions: []metav1.Condition{
					testCondition("new", metav1.ConditionTrue, "test-reason", "Test message.", now),
					testCondition("updated", metav1.ConditionTrue, "test-reason", "Test message.", now),
				},
			},
			want: want{
				workload: func() *kueue.Workload {
					wl := testWorkload("test-wl", "test-job", time.Now(), ptr.To(false))
					rv, _ := strconv.Atoi(wl.ResourceVersion)
					wl.ResourceVersion = strconv.Itoa(rv + 2) // 1 for spec and 1 for status updates.
					wl.Status.Conditions = []metav1.Condition{
						testCondition("new", metav1.ConditionTrue, "test-reason", "Test message.", now),
						testCondition("updated", metav1.ConditionTrue, "test-reason", "Test message.", now),
					}
					return wl
				}(),
			},
		},
		"Inactive": {
			args: args{
				ctx: t.Context(),
				clnt: testWorkloadClientBuilder().
					WithObjects(func() *kueue.Workload {
						wl := testWorkload("test-wl", "test-job", time.Now(), ptr.To(false))
						wl.Status.Conditions = []metav1.Condition{
							testCondition("updated", metav1.ConditionFalse, "test-reason", "Test message.", now),
						}
						return wl
					}()).
					WithStatusSubresource(testWorkload("test-wl", "test-job", time.Now(), ptr.To(true))).
					Build(),
				workloadSlice: testWorkload("test-wl", "test-job", time.Now(), ptr.To(false)),
				conditions: []metav1.Condition{
					testCondition("new", metav1.ConditionTrue, "test-reason", "Test message.", now),
					testCondition("updated", metav1.ConditionTrue, "test-reason", "Test message.", now),
				},
			},
			want: want{
				workload: func() *kueue.Workload {
					wl := testWorkload("test-wl", "test-job", time.Now(), ptr.To(false))
					rv, _ := strconv.Atoi(wl.ResourceVersion)
					wl.ResourceVersion = strconv.Itoa(rv + 1) // 1 for status updates.
					wl.Status.Conditions = []metav1.Condition{
						testCondition("new", metav1.ConditionTrue, "test-reason", "Test message.", now),
						testCondition("updated", metav1.ConditionTrue, "test-reason", "Test message.", now),
					}
					return wl
				}(),
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if err := Deactivate(tt.args.ctx, tt.args.clnt, tt.args.workloadSlice, tt.args.conditions...); (err != nil) != tt.want.err {
				t.Errorf("Deactivate() error = %v, wantErr %v", err, tt.want.err)
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
		"TwoWorkloadSlices_NewIsIncompatible": {
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
