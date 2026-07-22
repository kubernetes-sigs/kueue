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

package patching

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

var (
	errTestNotFound = apierrors.NewNotFound(
		schema.GroupResource{Group: "kueue.x-k8s.io", Resource: "workloads"},
		"test",
	)
	errTestConflict = apierrors.NewConflict(
		schema.GroupResource{Group: "kueue.x-k8s.io", Resource: "workloads"},
		"test",
		errors.New("object was modified"),
	)
)

func TestSetCheckState(t *testing.T) {
	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)
	t0 := metav1.NewTime(now.Add(-5 * time.Second))
	t1 := metav1.NewTime(now)
	ps1Updates := kueue.PodSetUpdate{
		Name: "ps1",
		Labels: map[string]string{
			"l1": "l1v",
		},
		Annotations: map[string]string{
			"a1": "a1v",
		},
		NodeSelector: map[string]string{
			"ns1": "ms1v",
		},
		Tolerations: []corev1.Toleration{
			{
				Key:               "t1",
				Operator:          corev1.TolerationOpEqual,
				Value:             "t1v",
				Effect:            corev1.TaintEffectNoSchedule,
				TolerationSeconds: ptr.To[int64](5),
			},
		},
	}

	cases := map[string]struct {
		origStates []kueue.AdmissionCheckState
		state      kueue.AdmissionCheckState
		wantStates []kueue.AdmissionCheckState
	}{
		"add new check": {
			origStates: []kueue.AdmissionCheckState{},
			state: kueue.AdmissionCheckState{
				Name:               "check1",
				State:              kueue.CheckStatePending,
				LastTransitionTime: *t0.DeepCopy(),
				Message:            "msg1",
				PodSetUpdates:      []kueue.PodSetUpdate{*ps1Updates.DeepCopy()},
			},
			wantStates: []kueue.AdmissionCheckState{
				{
					Name:               "check1",
					State:              kueue.CheckStatePending,
					LastTransitionTime: *t0.DeepCopy(),
					Message:            "msg1",
					PodSetUpdates:      []kueue.PodSetUpdate{*ps1Updates.DeepCopy()},
				},
			},
		},
		"update check": {
			origStates: []kueue.AdmissionCheckState{
				{
					Name:               "check1",
					State:              kueue.CheckStatePending,
					LastTransitionTime: *t0.DeepCopy(),
					Message:            "msg1",
					PodSetUpdates:      nil,
				},
				{
					Name:               "check2",
					State:              kueue.CheckStatePending,
					LastTransitionTime: *t0.DeepCopy(),
					Message:            "msg1",
					PodSetUpdates:      nil,
				},
			},
			state: kueue.AdmissionCheckState{
				Name:               "check1",
				State:              kueue.CheckStateReady,
				LastTransitionTime: *t1.DeepCopy(),
				Message:            "msg2",
				PodSetUpdates:      []kueue.PodSetUpdate{*ps1Updates.DeepCopy()},
			},
			wantStates: []kueue.AdmissionCheckState{
				{
					Name:               "check1",
					State:              kueue.CheckStateReady,
					LastTransitionTime: *t1.DeepCopy(),
					Message:            "msg2",
					PodSetUpdates:      []kueue.PodSetUpdate{*ps1Updates.DeepCopy()},
				},
				{
					Name:               "check2",
					State:              kueue.CheckStatePending,
					LastTransitionTime: *t0.DeepCopy(),
					Message:            "msg1",
					PodSetUpdates:      nil,
				},
			},
		},
		"add new check, no transition time": {
			origStates: []kueue.AdmissionCheckState{},
			state: kueue.AdmissionCheckState{
				Name:          "check1",
				State:         kueue.CheckStatePending,
				Message:       "msg1",
				PodSetUpdates: []kueue.PodSetUpdate{*ps1Updates.DeepCopy()},
			},
			wantStates: []kueue.AdmissionCheckState{
				{
					Name:          "check1",
					State:         kueue.CheckStatePending,
					Message:       "msg1",
					PodSetUpdates: []kueue.PodSetUpdate{*ps1Updates.DeepCopy()},
				},
			},
		},
		"update check, no transition time": {
			origStates: []kueue.AdmissionCheckState{
				{
					Name:               "check1",
					State:              kueue.CheckStatePending,
					LastTransitionTime: *t0.DeepCopy(),
					Message:            "msg1",
					PodSetUpdates:      nil,
				},
				{
					Name:               "check2",
					State:              kueue.CheckStatePending,
					LastTransitionTime: *t0.DeepCopy(),
					Message:            "msg1",
					PodSetUpdates:      nil,
				},
			},
			state: kueue.AdmissionCheckState{
				Name:          "check1",
				State:         kueue.CheckStateReady,
				Message:       "msg2",
				PodSetUpdates: []kueue.PodSetUpdate{*ps1Updates.DeepCopy()},
			},
			wantStates: []kueue.AdmissionCheckState{
				{
					Name:          "check1",
					State:         kueue.CheckStateReady,
					Message:       "msg2",
					PodSetUpdates: []kueue.PodSetUpdate{*ps1Updates.DeepCopy()},
				},
				{
					Name:               "check2",
					State:              kueue.CheckStatePending,
					LastTransitionTime: *t0.DeepCopy(),
					Message:            "msg1",
					PodSetUpdates:      nil,
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotStates := tc.origStates

			SetAdmissionCheckState(&gotStates, tc.state, fakeClock)

			opts := cmp.Options{}
			if tc.state.LastTransitionTime.IsZero() {
				opts = append(opts, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime"), cmpopts.EquateApproxTime(time.Second))

				if updatedCheck := admissioncheck.FindAdmissionCheck(gotStates, tc.state.Name); updatedCheck == nil {
					t.Error("Cannot find the updated check state")
				} else {
					if diff := cmp.Diff(metav1.NewTime(now), updatedCheck.LastTransitionTime, opts...); diff != "" {
						t.Errorf("Unexpected LastTransitionTime (- want/+ got):\n%s", diff)
					}
				}
			}

			if diff := cmp.Diff(tc.wantStates, gotStates, opts...); diff != "" {
				t.Errorf("Unexpected conditions after sync (- want/+ got):\n%s", diff)
			}
		})
	}
}

func TestPatchStatus(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	baseWl := utiltestingapi.MakeWorkload("test", metav1.NamespaceDefault).ResourceVersion("2")

	baseCond := metav1.Condition{
		Type:               "TestCondition",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: 1,
		LastTransitionTime: metav1.NewTime(now),
		Reason:             "By test",
		Message:            "By test",
	}

	type args struct {
		wl     *kueue.Workload
		update func(wl *kueue.Workload) (bool, error)
		opts   []PatchStatusOption
	}

	type want struct {
		wl  *kueue.Workload
		err error
	}

	tests := map[string]struct {
		skipApplyPatch bool
		skipMergePatch bool
		conflict       bool
		args           args
		want           want
	}{
		"update returns true": {
			args: args{
				wl: baseWl.DeepCopy(),
				update: func(wl *kueue.Workload) (bool, error) {
					apimeta.SetStatusCondition(&wl.Status.Conditions, baseCond)
					return true, nil
				},
			},
			want: want{
				wl: baseWl.Clone().ResourceVersion("3").Condition(baseCond).Obj(),
			},
		},
		"update returns true with conflict error": {
			conflict: true,
			args: args{
				wl: baseWl.DeepCopy(),
				update: func(wl *kueue.Workload) (bool, error) {
					apimeta.SetStatusCondition(&wl.Status.Conditions, baseCond)
					return true, nil
				},
			},
			want: want{
				wl:  baseWl.Clone().ResourceVersion("3").Obj(),
				err: errTestConflict,
			},
		},
		"update returns true with conflict error and WithLooseOnApply options": {
			skipMergePatch: true,
			conflict:       true,
			args: args{
				wl: baseWl.DeepCopy(),
				update: func(wl *kueue.Workload) (bool, error) {
					apimeta.SetStatusCondition(&wl.Status.Conditions, baseCond)
					return true, nil
				},
				opts: []PatchStatusOption{WithLooseOnApply()},
			},
			want: want{
				wl: baseWl.Clone().ResourceVersion("4").Condition(baseCond).Obj(),
			},
		},
		"update returns true with conflict error and WithRetryOnConflict options": {
			skipApplyPatch: true,
			conflict:       true,
			args: args{
				wl: baseWl.DeepCopy(),
				update: func(wl *kueue.Workload) (bool, error) {
					apimeta.SetStatusCondition(&wl.Status.Conditions, baseCond)
					return true, nil
				},
				opts: []PatchStatusOption{WithRetryOnConflict()},
			},
			want: want{
				wl: baseWl.Clone().ResourceVersion("4").Condition(baseCond).Obj(),
			},
		},
		"update returns false": {
			args: args{
				wl: baseWl.DeepCopy(),
				update: func(wl *kueue.Workload) (bool, error) {
					apimeta.SetStatusCondition(&wl.Status.Conditions, baseCond)
					return false, nil
				},
			},
			want: want{
				wl: baseWl.DeepCopy(),
			},
		},
		"update returns true with not found error": {
			conflict: true,
			args: args{
				wl: baseWl.DeepCopy(),
				update: func(wl *kueue.Workload) (bool, error) {
					apimeta.SetStatusCondition(&wl.Status.Conditions, baseCond)
					return true, errTestNotFound
				},
			},
			want: want{
				wl:  baseWl.DeepCopy(),
				err: errTestNotFound,
			},
		},
		"update returns false with not found error": {
			conflict: true,
			args: args{
				wl: baseWl.DeepCopy(),
				update: func(wl *kueue.Workload) (bool, error) {
					apimeta.SetStatusCondition(&wl.Status.Conditions, baseCond)
					return false, errTestNotFound
				},
			},
			want: want{
				wl:  baseWl.DeepCopy(),
				err: errTestNotFound,
			},
		},
	}
	for name, tc := range tests {
		if tc.skipMergePatch && tc.skipApplyPatch {
			t.Fatalf("skipMergePatch and skipApplyPatch both enabled")
		}

		for _, useMergePatch := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s with WorkloadRequestUseMergePatch enabled: %t", name, useMergePatch), func(t *testing.T) {
				switch {
				case tc.skipMergePatch && useMergePatch:
					t.Skip("Skipping test due to skipMergePatch being enabled")
				case tc.skipApplyPatch && !useMergePatch:
					t.Skip("Skipping test due to skipApplyPatch being enabled")
				}

				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, useMergePatch)
				ctx, _ := utiltesting.ContextWithLog(t)
				wl := tc.args.wl.DeepCopy()
				patched := false

				cl := utiltesting.NewClientBuilder().
					WithObjects(wl).
					WithStatusSubresource(&kueue.Workload{}).
					WithInterceptorFuncs(interceptor.Funcs{
						SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
							if tc.conflict {
								if _, ok := obj.(*kueue.Workload); ok && subResourceName == "status" && !patched {
									patched = true
									// Simulate concurrent modification by another controller
									wlCopy := wl.DeepCopy()
									if wlCopy.Labels == nil {
										wlCopy.Labels = make(map[string]string, 1)
									}
									wlCopy.Labels["test.kueue.x-k8s.io/timestamp"] = time.Now().String()
									if err := c.Update(ctx, wlCopy); err != nil {
										return err
									}
								}
							}
							return utiltesting.TreatSSAAsStrategicMerge(ctx, c, subResourceName, obj, patch, opts...)
						},
					}).
					Build()

				gotErr := PatchStatus(ctx, cl, wl, "test-owner", tc.args.update, tc.args.opts...)
				if diff := cmp.Diff(tc.want.err, gotErr); diff != "" {
					t.Errorf("Unexpected error (-want/+got)\n%s", diff)
				}

				updatedWl := &kueue.Workload{}
				if err := cl.Get(ctx, client.ObjectKeyFromObject(wl), updatedWl); err != nil {
					t.Fatalf("Failed obtaining updated object: %v", err)
				}

				if diff := cmp.Diff(tc.want.wl, updatedWl, cmpopts.EquateEmpty(), cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Labels")); diff != "" {
					t.Errorf("Unexpected status after updating (-want,+got):\n%s", diff)
				}
			})
		}
	}
}

func TestPatchAdmissionStatus(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)

	baseWl := utiltestingapi.MakeWorkload("test", metav1.NamespaceDefault).ResourceVersion("2")

	baseCond := metav1.Condition{
		Type:               kueue.WorkloadQuotaReserved,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: 1,
		LastTransitionTime: metav1.NewTime(now),
		Reason:             "By test",
		Message:            "By test",
	}

	type args struct {
		wl     *kueue.Workload
		update func(wl *kueue.Workload) (bool, error)
		opts   []PatchStatusOption
	}

	type want struct {
		wl  *kueue.Workload
		err error
	}

	tests := map[string]struct {
		skipApplyPatch bool
		skipMergePatch bool
		conflict       bool
		args           args
		want           want
	}{
		"update returns true": {
			args: args{
				wl: baseWl.DeepCopy(),
				update: func(wl *kueue.Workload) (bool, error) {
					apimeta.SetStatusCondition(&wl.Status.Conditions, baseCond)
					return true, nil
				},
			},
			want: want{
				wl: baseWl.Clone().ResourceVersion("3").Condition(baseCond).Obj(),
			},
		},
		"update returns true with unmanaged condition": {
			skipMergePatch: true,
			args: args{
				wl: baseWl.DeepCopy(),
				update: func(wl *kueue.Workload) (bool, error) {
					apimeta.SetStatusCondition(&wl.Status.Conditions, metav1.Condition{
						Type:               "TestCondition",
						Status:             metav1.ConditionTrue,
						ObservedGeneration: 1,
						LastTransitionTime: metav1.NewTime(now),
						Reason:             "By test",
						Message:            "By test",
					})
					return true, nil
				},
			},
			want: want{
				wl: baseWl.Clone().ResourceVersion("3").Obj(),
			},
		},
		"update returns true with conflict error": {
			conflict: true,
			args: args{
				wl: baseWl.DeepCopy(),
				update: func(wl *kueue.Workload) (bool, error) {
					apimeta.SetStatusCondition(&wl.Status.Conditions, baseCond)
					return true, nil
				},
			},
			want: want{
				wl:  baseWl.Clone().ResourceVersion("3").Obj(),
				err: errTestConflict,
			},
		},
		"update returns true with conflict error and WithLooseOnApply options": {
			skipMergePatch: true,
			conflict:       true,
			args: args{
				wl: baseWl.DeepCopy(),
				update: func(wl *kueue.Workload) (bool, error) {
					apimeta.SetStatusCondition(&wl.Status.Conditions, baseCond)
					return true, nil
				},
				opts: []PatchStatusOption{WithLooseOnApply()},
			},
			want: want{
				wl: baseWl.Clone().ResourceVersion("4").Condition(baseCond).Obj(),
			},
		},
		"update returns true with conflict error and WithRetryOnConflict options": {
			skipApplyPatch: true,
			conflict:       true,
			args: args{
				wl: baseWl.DeepCopy(),
				update: func(wl *kueue.Workload) (bool, error) {
					apimeta.SetStatusCondition(&wl.Status.Conditions, baseCond)
					return true, nil
				},
				opts: []PatchStatusOption{WithRetryOnConflict()},
			},
			want: want{
				wl: baseWl.Clone().ResourceVersion("4").Condition(baseCond).Obj(),
			},
		},
		"update returns false": {
			args: args{
				wl: baseWl.DeepCopy(),
				update: func(wl *kueue.Workload) (bool, error) {
					apimeta.SetStatusCondition(&wl.Status.Conditions, baseCond)
					return false, nil
				},
			},
			want: want{
				wl: baseWl.DeepCopy(),
			},
		},
		"update returns true with not found error": {
			conflict: true,
			args: args{
				wl: baseWl.DeepCopy(),
				update: func(wl *kueue.Workload) (bool, error) {
					apimeta.SetStatusCondition(&wl.Status.Conditions, baseCond)
					return true, errTestNotFound
				},
			},
			want: want{
				wl:  baseWl.DeepCopy(),
				err: errTestNotFound,
			},
		},
		"update returns false with not found error": {
			conflict: true,
			args: args{
				wl: baseWl.DeepCopy(),
				update: func(wl *kueue.Workload) (bool, error) {
					apimeta.SetStatusCondition(&wl.Status.Conditions, baseCond)
					return false, errTestNotFound
				},
			},
			want: want{
				wl:  baseWl.DeepCopy(),
				err: errTestNotFound,
			},
		},
	}
	for name, tc := range tests {
		if tc.skipMergePatch && tc.skipApplyPatch {
			t.Fatalf("skipMergePatch and skipApplyPatch both enabled")
		}

		for _, useMergePatch := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s with WorkloadRequestUseMergePatch enabled: %t", name, useMergePatch), func(t *testing.T) {
				switch {
				case tc.skipMergePatch && useMergePatch:
					t.Skip("Skipping test due to skipMergePatch being enabled")
				case tc.skipApplyPatch && !useMergePatch:
					t.Skip("Skipping test due to skipApplyPatch being enabled")
				}

				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, useMergePatch)
				ctx, _ := utiltesting.ContextWithLog(t)
				wl := tc.args.wl.DeepCopy()
				patched := false

				cl := utiltesting.NewClientBuilder().
					WithObjects(wl).
					WithStatusSubresource(&kueue.Workload{}).
					WithInterceptorFuncs(interceptor.Funcs{
						SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
							if tc.conflict {
								if _, ok := obj.(*kueue.Workload); ok && subResourceName == "status" && !patched {
									patched = true
									// Simulate concurrent modification by another controller
									wlCopy := wl.DeepCopy()
									if wlCopy.Labels == nil {
										wlCopy.Labels = make(map[string]string, 1)
									}
									wlCopy.Labels["test.kueue.x-k8s.io/timestamp"] = time.Now().String()
									if err := c.Update(ctx, wlCopy); err != nil {
										return err
									}
								}
							}
							return utiltesting.TreatSSAAsStrategicMerge(ctx, c, subResourceName, obj, patch, opts...)
						},
					}).
					Build()

				gotErr := PatchAdmissionStatus(ctx, cl, wl, fakeClock, tc.args.update, tc.args.opts...)
				if diff := cmp.Diff(tc.want.err, gotErr); diff != "" {
					t.Errorf("Unexpected error (-want/+got)\n%s", diff)
				}

				updatedWl := &kueue.Workload{}
				if err := cl.Get(ctx, client.ObjectKeyFromObject(wl), updatedWl); err != nil {
					t.Fatalf("Failed obtaining updated object: %v", err)
				}

				if diff := cmp.Diff(tc.want.wl, updatedWl, cmpopts.EquateEmpty(), cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Labels")); diff != "" {
					t.Errorf("Unexpected status after updating (-want,+got):\n%s", diff)
				}
			})
		}
	}
}
