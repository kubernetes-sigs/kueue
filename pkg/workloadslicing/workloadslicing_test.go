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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
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
					Annotations: map[string]string{},
				},
			},
		},
		"Enabled": {
			args: args{
				object: &batchv1.Job{
					Annotations: map[string]string{
						EnabledAnnotationKey: EnabledAnnotationValue,
					},
				},
			},
			want: true,
		},
		"NotEnabled": {
			args: args{
				object: &batchv1.Job{
					Annotations: map[string]string{
						EnabledAnnotationKey: "True", // <-- value is case sensitive.
					},
				},
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			// Always false when feature is disabled.
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, false)
			if got := Enabled(tt.args.object); got {
				t.Error("Enabled() = true, want false when feature is not enabled")
			}
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
			if got := Enabled(tt.args.object); got != tt.want {
				t.Errorf("Enabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestListPodsForWorkloadSlice(t *testing.T) {
	errListPods := errors.New("list pods failed")
	basePod := testingpod.MakePod("", "ns")
	// Match the omitted fields after the fake client's JSON round trip.
	basePod.Spec.Containers[0].Resources = corev1.ResourceRequirements{}
	basePod.Spec.SchedulingGates = nil
	originPod := basePod.Clone().Name("origin-pod").
		Annotation(kueue.WorkloadSliceNameAnnotation, "origin").
		Annotation(kueue.WorkloadAnnotation, "origin").
		Label("role", "worker").NodeName("node-a").Obj()
	replacementPod := basePod.Clone().Name("replacement-pod").
		Annotation(kueue.WorkloadSliceNameAnnotation, "origin").
		Annotation(kueue.WorkloadAnnotation, "replacement").
		Label("role", "worker").NodeName("node-a").Obj()
	succeededPod := basePod.Clone().Name("succeeded-pod").
		Annotation(kueue.WorkloadSliceNameAnnotation, "origin").
		Label("role", "worker").NodeName("node-b").StatusPhase(corev1.PodSucceeded).Obj()
	failedPod := basePod.Clone().Name("failed-pod").
		Annotation(kueue.WorkloadSliceNameAnnotation, "origin").
		Label("role", "launcher").NodeName("node-a").StatusPhase(corev1.PodFailed).Obj()
	regularPod := basePod.Clone().Name("regular-pod").
		Annotation(kueue.WorkloadAnnotation, "regular").
		Label("role", "worker").NodeName("node-a").Obj()
	pods := []client.Object{
		originPod,
		replacementPod,
		succeededPod,
		failedPod,
		regularPod,
		basePod.Clone().Name("other-namespace").Namespace("other").
			Annotation(kueue.WorkloadSliceNameAnnotation, "origin").
			Label("role", "worker").NodeName("node-a").Obj(),
		basePod.Clone().Name("other-slice-pod").
			Annotation(kueue.WorkloadAnnotation, "origin").
			Annotation(kueue.WorkloadSliceNameAnnotation, "other").
			Label("role", "worker").NodeName("node-a").Obj(),
		basePod.Clone().Name("unrelated-pod").
			Label("role", "worker").NodeName("node-a").Obj(),
	}
	testCases := map[string]struct {
		sliceName   string
		listOptions []client.ListOption
		wantPods    []*corev1.Pod
		wantErr     error
	}{
		"all pods in the slice chain, including terminal pods": {
			sliceName: "origin",
			wantPods:  []*corev1.Pod{originPod, replacementPod, succeededPod, failedPod},
		},
		"additional label selector": {
			sliceName:   "origin",
			listOptions: []client.ListOption{client.MatchingLabels{"role": "worker"}},
			wantPods:    []*corev1.Pod{originPod, replacementPod, succeededPod},
		},
		"matching fields": {
			sliceName:   "origin",
			listOptions: []client.ListOption{client.MatchingFields{tasindexer.PodNodeNameKey: "node-a"}},
			wantPods:    []*corev1.Pod{originPod, replacementPod, failedPod},
		},
		"field and label selectors": {
			sliceName: "origin",
			listOptions: []client.ListOption{
				client.MatchingFields{tasindexer.PodNodeNameKey: "node-a"},
				client.MatchingLabels{"role": "worker"},
			},
			wantPods: []*corev1.Pod{originPod, replacementPod},
		},
		"matching fields selector": {
			sliceName: "origin",
			listOptions: []client.ListOption{client.MatchingFieldsSelector{
				Selector: fields.OneTermEqualSelector(tasindexer.PodNodeNameKey, "node-a"),
			}},
			wantPods: []*corev1.Pod{originPod, replacementPod, failedPod},
		},
		"list options with field and label selectors": {
			sliceName: "origin",
			listOptions: []client.ListOption{&client.ListOptions{
				FieldSelector: fields.OneTermEqualSelector(tasindexer.PodNodeNameKey, "node-a"),
				LabelSelector: labels.SelectorFromSet(labels.Set{"role": "worker"}),
			}},
			wantPods: []*corev1.Pod{originPod, replacementPod},
		},
		"empty field selector": {
			sliceName:   "origin",
			listOptions: []client.ListOption{client.MatchingFields{}},
			wantPods:    []*corev1.Pod{originPod, replacementPod, succeededPod, failedPod},
		},
		"no pods match the node": {
			sliceName:   "origin",
			listOptions: []client.ListOption{client.MatchingFields{tasindexer.PodNodeNameKey: "unknown-node"}},
			wantPods:    []*corev1.Pod{},
		},
		"conflicting slice selector": {
			sliceName:   "origin",
			listOptions: []client.ListOption{client.MatchingFields{indexer.WorkloadSliceNameKey: "regular"}},
			wantPods:    []*corev1.Pod{},
		},
		"regular workload uses the workload annotation": {
			sliceName: "regular",
			wantPods:  []*corev1.Pod{regularPod},
		},
		"no matching pods": {
			sliceName: "missing",
			wantPods:  []*corev1.Pod{},
		},
		"list failure is returned": {
			sliceName: "origin",
			wantErr:   errListPods,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			builder := utiltesting.NewClientBuilder().
				WithObjects(pods...).
				WithIndex(&corev1.Pod{}, indexer.WorkloadSliceNameKey, indexer.IndexPodWorkloadSliceName).
				WithInterceptorFuncs(interceptor.Funcs{
					List: func(ctx context.Context, c client.WithWatch, objs client.ObjectList, opts ...client.ListOption) error {
						if _, ok := objs.(*corev1.PodList); ok && errors.Is(tc.wantErr, errListPods) {
							return errListPods
						}
						return c.List(ctx, objs, opts...)
					},
				})
			if err := tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(builder)); err != nil {
				t.Fatalf("Failed to set up indexes: %v", err)
			}
			gotPods, err := ListPodsForWorkloadSlice(ctx, builder.Build(), "ns", tc.sliceName, tc.listOptions...)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantPods, gotPods,
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
				cmpopts.SortSlices(func(a, b *corev1.Pod) bool { return a.Name < b.Name }),
			); diff != "" {
				t.Errorf("Unexpected pods (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestFindActiveWorkload(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	errGetWorkload := errors.New("get workload failed")
	errListWorkload := errors.New("list workloads failed")
	origin := utiltestingapi.MakeWorkload("origin", "ns").
		Request(corev1.ResourceCPU, "1").
		Annotation(EnabledAnnotationKey, EnabledAnnotationValue).
		Creation(now.Add(-time.Minute)).
		SimpleReserveQuota("cq", "flavor", now).
		AdmittedAt(true, now)
	replacement := utiltestingapi.MakeWorkload("replacement", "ns").
		Request(corev1.ResourceCPU, "1").
		Annotation(EnabledAnnotationKey, EnabledAnnotationValue).
		Annotation(kueue.WorkloadSliceNameAnnotation, "origin").
		Creation(now).
		SimpleReserveQuota("cq", "flavor", now).
		AdmittedAt(true, now)
	variant := utiltestingapi.MakeWorkload("variant", "ns").
		Request(corev1.ResourceCPU, "1").
		Annotation(EnabledAnnotationKey, EnabledAnnotationValue).
		Annotation(kueue.WorkloadSliceNameAnnotation, "origin").
		OwnerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "replacement", "parent").
		Creation(now.Add(time.Second)).
		SimpleReserveQuota("cq", "flavor", now).
		AdmittedAt(true, now)

	testCases := map[string]struct {
		featureGates    map[featuregate.Feature]bool
		excludeVariants bool
		requestName     string
		workloads       []*kueue.Workload
		wantWorkload    *kueue.Workload
		wantError       error
	}{
		"ordinary workload does not require the slice index": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: false},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("origin", "ns").
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			wantWorkload: utiltestingapi.MakeWorkload("origin", "ns").
				Request(corev1.ResourceCPU, "1").
				Obj(),
		},
		"ordinary workload is not redirected with elastic jobs enabled": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("origin", "ns").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				replacement.Obj(),
			},
			wantWorkload: utiltestingapi.MakeWorkload("origin", "ns").
				Request(corev1.ResourceCPU, "1").
				Obj(),
		},
		"disabled elastic jobs retain the requested slice": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: false},
			workloads:    []*kueue.Workload{origin.Obj(), replacement.Obj()},
			wantWorkload: origin.Obj(),
		},
		"missing workload without elastic jobs does not require the slice index": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: false},
		},
		"missing workload without a replacement": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
		},
		"get error is returned": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			wantError:    errGetWorkload,
		},
		"list error after a missing origin is returned": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			wantError:    errListWorkload,
		},
		"list error after an existing origin is returned": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			workloads:    []*kueue.Workload{origin.Obj()},
			wantError:    errListWorkload,
		},
		"latest admitted slice replaces an admitted origin": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			workloads:    []*kueue.Workload{origin.Obj(), replacement.Obj()},
			wantWorkload: replacement.Obj(),
		},
		"finished origin resolves to the replacement": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			workloads:    []*kueue.Workload{origin.Clone().FinishedAt(now).Obj(), replacement.Obj()},
			wantWorkload: replacement.Obj(),
		},
		"deleted origin resolves to the replacement": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			workloads:    []*kueue.Workload{replacement.Obj()},
			wantWorkload: replacement.Obj(),
		},
		"request for a replacement uses the chain annotation": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			requestName:  "replacement",
			workloads:    []*kueue.Workload{origin.Obj(), replacement.Obj()},
			wantWorkload: replacement.Obj(),
		},
		"non-admitted workload is retained for condition reset": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			workloads:    []*kueue.Workload{origin.Clone().AdmittedAt(false, now).Obj()},
			wantWorkload: origin.Clone().AdmittedAt(false, now).Obj(),
		},
		"finished workload is retained when there is no active slice": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			workloads:    []*kueue.Workload{origin.Clone().FinishedAt(now).Obj()},
			wantWorkload: origin.Clone().FinishedAt(now).Obj(),
		},
		"evicted replacement is ignored even before quota release": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			workloads:    []*kueue.Workload{origin.Obj(), replacement.Clone().EvictedAt(now).Obj()},
			wantWorkload: origin.Obj(),
		},
		"finished replacement is ignored": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			workloads:    []*kueue.Workload{origin.Obj(), replacement.Clone().FinishedAt(now).Obj()},
			wantWorkload: origin.Obj(),
		},
		"quota reserved but not admitted replacement is ignored": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			workloads:    []*kueue.Workload{origin.Obj(), replacement.Clone().AdmittedAt(false, now).Obj()},
			wantWorkload: origin.Obj(),
		},
		"replacement in another namespace is ignored": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			workloads: []*kueue.Workload{
				origin.Obj(),
				utiltestingapi.MakeWorkload("replacement", "other").
					Request(corev1.ResourceCPU, "1").
					Annotation(kueue.WorkloadSliceNameAnnotation, "origin").
					SimpleReserveQuota("cq", "flavor", now).
					AdmittedAt(true, now).
					Obj(),
			},
			wantWorkload: origin.Obj(),
		},
		"tracker excludes variants": {
			featureGates: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices: true,
				features.ConcurrentAdmission:          true,
			},
			workloads:       []*kueue.Workload{origin.Obj(), replacement.Obj(), variant.Obj()},
			excludeVariants: true,
			wantWorkload:    replacement.Obj(),
		},
		"ungaters retain variant selection": {
			featureGates: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices: true,
				features.ConcurrentAdmission:          true,
			},
			workloads:    []*kueue.Workload{origin.Obj(), replacement.Obj(), variant.Obj()},
			wantWorkload: variant.Obj(),
		},
		"concurrent admission disabled retains variant selection": {
			featureGates: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices: true,
				features.ConcurrentAdmission:          false,
			},
			workloads:       []*kueue.Workload{origin.Obj(), replacement.Obj(), variant.Obj()},
			excludeVariants: true,
			wantWorkload:    variant.Obj(),
		},
		"same creation timestamp is ordered by UID": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			workloads:    []*kueue.Workload{origin.Clone().Creation(now).UID("z").Obj(), replacement.Clone().UID("a").Obj()},
			wantWorkload: origin.Clone().Creation(now).UID("z").Obj(),
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, _ := utiltesting.ContextWithLog(t)
			builder := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*kueue.Workload); ok && errors.Is(tc.wantError, errGetWorkload) {
						return errGetWorkload
					}
					return c.Get(ctx, key, obj, opts...)
				},
				List: func(ctx context.Context, c client.WithWatch, objs client.ObjectList, opts ...client.ListOption) error {
					if _, ok := objs.(*kueue.WorkloadList); ok && errors.Is(tc.wantError, errListWorkload) {
						return errListWorkload
					}
					return c.List(ctx, objs, opts...)
				},
			})
			if tc.featureGates[features.ElasticJobsViaWorkloadSlices] {
				builder.WithIndex(&kueue.Workload{}, indexer.WorkloadSliceNameKey, indexer.IndexWorkloadSliceName)
			}
			for _, wl := range tc.workloads {
				builder.WithObjects(wl)
			}
			key := types.NamespacedName{Namespace: "ns", Name: "origin"}
			if tc.requestName != "" {
				key.Name = tc.requestName
			}
			got, err := FindActiveWorkload(ctx, builder.Build(), key, tc.excludeVariants)
			if diff := cmp.Diff(tc.wantError, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantWorkload, got, cmpopts.IgnoreFields(kueue.Workload{}, "TypeMeta", "ObjectMeta.ResourceVersion")); diff != "" {
				t.Errorf("Unexpected Workload (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestFindLatestAdmittedWorkload(t *testing.T) {
	fakeClock := testingclock.NewFakeClock(time.Now().Truncate(time.Second))
	errListWorkloads := errors.New("list workloads failed")

	origin := utiltestingapi.MakeWorkload("origin", "ns").
		Annotation(EnabledAnnotationKey, EnabledAnnotationValue).
		Creation(fakeClock.Now().Add(-time.Minute)).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), fakeClock.Now().Add(-time.Minute)).
		AdmittedAt(true, fakeClock.Now().Add(-time.Minute))
	replacement := utiltestingapi.MakeWorkload("replacement", "ns").
		Annotation(EnabledAnnotationKey, EnabledAnnotationValue).
		Annotation(kueue.WorkloadSliceNameAnnotation, "origin").
		Creation(fakeClock.Now()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), fakeClock.Now()).
		AdmittedAt(true, fakeClock.Now())
	variant := utiltestingapi.MakeWorkload("variant", "ns").
		Annotation(EnabledAnnotationKey, EnabledAnnotationValue).
		Annotation(kueue.WorkloadSliceNameAnnotation, "origin").
		Creation(fakeClock.Now().Add(time.Second)).
		OwnerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "replacement", "parent-uid").
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), fakeClock.Now()).
		AdmittedAt(true, fakeClock.Now())

	testCases := map[string]struct {
		featureGates    map[featuregate.Feature]bool
		excludeVariants bool
		workload        *kueue.Workload
		workloads       []*kueue.Workload
		wantName        string
		wantErr         error
	}{
		"nil workload": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
		},
		"origin without the elastic annotation retains the slice lookup": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			workload:     utiltestingapi.MakeWorkload("origin", "ns").Obj(),
			workloads:    []*kueue.Workload{origin.Obj(), replacement.Obj()},
			wantName:     "replacement",
		},
		"elastic feature gate disabled retains the slice lookup": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: false},
			workload:     origin.Obj(),
			workloads:    []*kueue.Workload{origin.Obj(), replacement.Obj()},
			wantName:     "replacement",
		},
		"no admitted slice": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			workload:     origin.Obj(),
		},
		"list failure is returned": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			workload:     origin.Obj(),
			wantErr:      errListWorkloads,
		},
		"finished origin resolves to the replacement": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			workload: origin.Clone().
				FinishedAt(fakeClock.Now()).
				Obj(),
			workloads: []*kueue.Workload{origin.Clone().
				FinishedAt(fakeClock.Now()).
				Obj(), replacement.Obj()},
			wantName: "replacement",
		},
		"latest admitted replacement is selected": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			workload:     origin.Obj(),
			workloads:    []*kueue.Workload{origin.Obj(), replacement.Obj()},
			wantName:     "replacement",
		},
		"newer admitted variant does not hide the replacement slice": {
			excludeVariants: true,
			featureGates: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices:       true,
				features.WaitForPodsReadyUnscheduledTimeout: true,
				features.ConcurrentAdmission:                true,
			},
			workload: origin.Obj(),
			workloads: []*kueue.Workload{
				origin.Clone().
					FinishedAt(fakeClock.Now()).
					Obj(),
				replacement.Obj(),
				variant.Obj(),
			},
			wantName: "replacement",
		},
		"scheduling feature gate disabled preserves variant selection": {
			featureGates: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices:       true,
				features.WaitForPodsReadyUnscheduledTimeout: false,
				features.ConcurrentAdmission:                true,
			},
			workload:  origin.Obj(),
			workloads: []*kueue.Workload{origin.Obj(), replacement.Obj(), variant.Obj()},
			wantName:  "variant",
		},
		"ungater lookup preserves variant selection with the scheduling gate enabled": {
			featureGates: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices:       true,
				features.WaitForPodsReadyUnscheduledTimeout: true,
				features.ConcurrentAdmission:                true,
			},
			workload:  origin.Obj(),
			workloads: []*kueue.Workload{origin.Obj(), replacement.Obj(), variant.Obj()},
			wantName:  "variant",
		},
		"concurrent admission disabled preserves variant selection": {
			excludeVariants: true,
			featureGates: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices:       true,
				features.WaitForPodsReadyUnscheduledTimeout: true,
				features.ConcurrentAdmission:                false,
			},
			workload:  origin.Obj(),
			workloads: []*kueue.Workload{origin.Obj(), replacement.Obj(), variant.Obj()},
			wantName:  "variant",
		},
		"elastic feature gate disabled still excludes variants": {
			featureGates: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices: false,
				features.ConcurrentAdmission:          true,
			},
			excludeVariants: true,
			workload:        origin.Obj(),
			workloads:       []*kueue.Workload{origin.Obj(), replacement.Obj(), variant.Obj()},
			wantName:        "replacement",
		},
		"evicted replacement is ignored": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			workload:     replacement.Obj(),
			workloads: []*kueue.Workload{origin.Obj(), replacement.Clone().
				EvictedAt(fakeClock.Now()).
				Obj()},
			wantName: "origin",
		},
		"finished replacement is ignored": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			workload:     replacement.Obj(),
			workloads: []*kueue.Workload{origin.Obj(), replacement.Clone().
				FinishedAt(fakeClock.Now()).
				Obj()},
			wantName: "origin",
		},
		"replacement in another namespace is ignored": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			workload:     origin.Obj(),
			workloads: []*kueue.Workload{origin.Obj(),
				utiltestingapi.MakeWorkload("replacement", "other").
					Annotation(kueue.WorkloadSliceNameAnnotation, "origin").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), fakeClock.Now()).
					AdmittedAt(true, fakeClock.Now()).
					Obj()},
			wantName: "origin",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, _ := utiltesting.ContextWithLog(t)
			builder := utiltesting.NewClientBuilder().
				WithIndex(&kueue.Workload{}, indexer.WorkloadSliceNameKey, indexer.IndexWorkloadSliceName).
				WithInterceptorFuncs(interceptor.Funcs{
					List: func(ctx context.Context, c client.WithWatch, objs client.ObjectList, opts ...client.ListOption) error {
						if _, ok := objs.(*kueue.WorkloadList); ok && errors.Is(tc.wantErr, errListWorkloads) {
							return errListWorkloads
						}
						return c.List(ctx, objs, opts...)
					},
				})
			for _, wl := range tc.workloads {
				builder = builder.WithObjects(wl)
			}
			got, err := FindLatestAdmittedWorkload(ctx, builder.Build(), tc.workload, tc.excludeVariants)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			var gotName string
			if got != nil {
				gotName = got.Name
			}
			if diff := cmp.Diff(tc.wantName, gotName); diff != "" {
				t.Errorf("Unexpected workload name (-want,+got):\n%s", diff)
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
					Annotations: make(map[string]string),
				},
			},
		},
		"Found": {
			args: args{
				wl: &kueue.Workload{
					Annotations: map[string]string{WorkloadSliceReplacementFor: string(testReference)},
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
		Name: "test",
		UID:  uuid.NewUUID(),
	}
)

func testWorkloadClientBuilder() *fake.ClientBuilder {
	testSchema := runtime.NewScheme()
	_ = kueue.AddToScheme(testSchema)
	return fake.NewClientBuilder().
		WithScheme(testSchema).
		WithStatusSubresource(&kueue.Workload{}).
		WithIndex(&kueue.Workload{}, indexer.OwnerReferenceIndexKey(testJobGVK), indexer.WorkloadOwnerIndexFunc(testJobGVK))
}

func testWorkload(name, jobName string, jobUID types.UID, created time.Time) *utiltestingapi.WorkloadWrapper {
	return utiltestingapi.MakeWorkload(name, "default").
		OwnerReference(testJobGVK, jobName, string(jobUID)).
		Creation(created).
		ResourceVersion("1").
		Request(corev1.ResourceCPU, "100m")
}

func TestFindNotFinishedWorkloads(t *testing.T) {
	type args struct {
		clnt         client.Client
		jobObject    client.Object
		jobObjectGVK schema.GroupVersionKind
	}

	// test "constants".
	now := time.Now()
	errListWorkloads := errors.New("list workloads failed")

	// test cases.
	tests := map[string]struct {
		args    args
		want    []kueue.Workload
		wantErr error
	}{
		"ListFailure": {
			args: args{
				clnt: utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
					List: func(_ context.Context, _ client.WithWatch, objs client.ObjectList, _ ...client.ListOption) error {
						if _, ok := objs.(*kueue.WorkloadList); ok {
							return errListWorkloads
						}
						return nil
					},
				}).Build(),
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			wantErr: errListWorkloads,
		},
		"EmptyList": {
			args: args{
				clnt:         testWorkloadClientBuilder().Build(),
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: []kueue.Workload{},
		},
		"SortedAndFiltered": {
			args: args{
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
		// Regression test for https://github.com/kubernetes-sigs/kueue/issues/11166:
		// When two workloads have identical timestamps and neither's ReplacementFor
		// annotation points to the other (e.g., v1 and v3 from a v1→v2→v3 chain where
		// v2 was finished), the comparator must return 0 rather than 1 in both
		// directions, which would violate the antisymmetry required by slices.SortFunc.
		"TwoActiveWorkloads_TimestampCollision_NeitherReplacesOther": {
			args: args{
				clnt: testWorkloadClientBuilder().WithLists(&kueue.WorkloadList{
					Items: []kueue.Workload{
						// test-23 (v3) has ReplacementFor pointing to test-22 (v2), not test-21 (v1).
						*testWorkload("test-23", testJobObject.Name, testJobObject.UID, now).
							ResourceVersion("300").
							Annotation(WorkloadSliceReplacementFor, string(workload.NewReference("default", "test-22"))).
							Obj(),
						// test-21 (v1) has no ReplacementFor annotation (it was the first slice).
						*testWorkload("test-21", testJobObject.Name, testJobObject.UID, now).
							ResourceVersion("100").
							Obj(),
					},
				}).Build(),
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			// Both workloads should be returned without a panic.
			// Since neither replaces the other, the comparator returns 0 (equal).
			want: []kueue.Workload{
				*testWorkload("test-21", testJobObject.Name, testJobObject.UID, now).
					ResourceVersion("100").
					Obj(),
				*testWorkload("test-23", testJobObject.Name, testJobObject.UID, now).
					ResourceVersion("300").
					Annotation(WorkloadSliceReplacementFor, string(workload.NewReference("default", "test-22"))).
					Obj(),
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			got, err := FindNotFinishedWorkloads(ctx, tt.args.clnt, tt.args.jobObject, tt.args.jobObjectGVK)
			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("FindActiveSlices() error (-want,+got):\n%s", diff)
				return
			}
			if diff := cmp.Diff(got, tt.want, cmpopts.EquateApproxTime(time.Second)); diff != "" {
				t.Errorf("FindActiveSlices() got(-),want(+): %s", diff)
			}
		})
	}
}

func TestEnsureWorkloadSlices(t *testing.T) {
	type args struct {
		clnt         client.Client
		jobPodSets   []kueue.PodSet
		jobObject    client.Object
		jobObjectGVK schema.GroupVersionKind
	}
	type want struct {
		workload          *kueue.Workload
		compatible        bool
		error             error
		finishedWorkloads map[string]string
	}
	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)
	fiveMinutesAgo := now.Add(-5 * time.Minute)
	testWorkload := utiltestingapi.MakeWorkload("", testJobObject.Namespace).
		OwnerReference(testJobGVK, testJobObject.Name, "")

	errTest := errors.New("test error")

	tests := map[string]struct {
		args args
		want want
	}{
		"FailedListWorkloads": {
			args: args{
				clnt: func() client.Client {
					listCalls := 0
					return testWorkloadClientBuilder().
						WithInterceptorFuncs(interceptor.Funcs{
							List: func(ctx context.Context, c client.WithWatch, obj client.ObjectList, opts ...client.ListOption) error {
								listCalls++
								if listCalls == 1 {
									return errTest
								}
								return c.List(ctx, obj, opts...)
							},
						}).
						Build()
				}(),
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				error:      errTest,
				compatible: true,
			},
		},
		// No workloads.
		"NoWorkloadSlices": {
			args: args{
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
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).Build(),
				jobPodSets:   []kueue.PodSet{*utiltestingapi.MakePodSet("different-name", 1).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
		},
		"OneWorkloadSlice_CurrentWorkload": {
			args: args{
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).Build(),
				jobPodSets:   []kueue.PodSet{*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				workload: utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("1").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
				compatible: true,
			},
		},
		"OneWorkloadSlice_ReservedQuota_ScaleUp": {
			args: args{
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuotaAt(utiltestingapi.MakeAdmission("default").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
						Obj()).Build(),
				jobPodSets:   []kueue.PodSet{*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
			},
		},
		"OneWorkloadSlice_ReservedQuota_ScaleUp_MultiplePodSets": {
			args: args{
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						PodSets(
							*utiltestingapi.MakePodSet("scale-up", 3).Request(corev1.ResourceCPU, "1").Obj(),
							*utiltestingapi.MakePodSet("scale-down", 3).Request(corev1.ResourceCPU, "1").Obj(),
							*utiltestingapi.MakePodSet("stay-the-same", 3).Request(corev1.ResourceCPU, "1").Obj(),
						).
						ReserveQuotaAt(utiltestingapi.MakeAdmission("default").
							PodSets(
								utiltestingapi.MakePodSetAssignment("scaled-up").Assignment(corev1.ResourceCPU, "default", "1").Count(3).Obj(),
								utiltestingapi.MakePodSetAssignment("scale-down").Assignment(corev1.ResourceCPU, "default", "1").Count(3).Obj(),
								utiltestingapi.MakePodSetAssignment("stay-the-same").Assignment(corev1.ResourceCPU, "default", "1").Count(3).Obj(),
							).
							Obj(), now).
						Obj()).Build(),
				jobPodSets: []kueue.PodSet{
					*utiltestingapi.MakePodSet("scale-up", 4).Request(corev1.ResourceCPU, "1").Obj(),      // <-- scaled-up.
					*utiltestingapi.MakePodSet("stay-the-same", 3).Request(corev1.ResourceCPU, "1").Obj(), // <-- stayed the same.
					*utiltestingapi.MakePodSet("scale-down", 1).Request(corev1.ResourceCPU, "1").Obj(),    // <-- scaled-down.
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
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuotaAt(utiltestingapi.MakeAdmission("default").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").Count(3).Obj()).Obj(), now).
						Obj()).Build(),
				jobPodSets:   []kueue.PodSet{*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				workload: utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("2").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("default").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").Count(3).Obj()).Obj(), now).
					Obj(),
			},
		},
		"OneWorkloadSlice_ReservedQuota_ScaleDown_MultiplePodSets": {
			args: args{
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						PodSets(
							*utiltestingapi.MakePodSet("scale-down", 3).Request(corev1.ResourceCPU, "1").Obj(),
							*utiltestingapi.MakePodSet("stay-the-same", 3).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuotaAt(utiltestingapi.MakeAdmission("default").
							PodSets(
								utiltestingapi.MakePodSetAssignment("scale-down").Assignment(corev1.ResourceCPU, "default", "1").Count(3).Obj(),
								utiltestingapi.MakePodSetAssignment("stay-the-same").Assignment(corev1.ResourceCPU, "default", "1").Count(3).Obj(),
							).
							Obj(), now).
						Obj()).Build(),
				jobPodSets: []kueue.PodSet{
					*utiltestingapi.MakePodSet("scale-down", 1).Request(corev1.ResourceCPU, "1").Obj(),    // <-- scaled-down.
					*utiltestingapi.MakePodSet("stay-the-same", 3).Request(corev1.ResourceCPU, "1").Obj(), // <-- stayed the same.
				},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				workload: utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("2").
					PodSets(
						*utiltestingapi.MakePodSet("scale-down", 1).Request(corev1.ResourceCPU, "1").Obj(),
						*utiltestingapi.MakePodSet("stay-the-same", 3).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("default").
						PodSets(
							utiltestingapi.MakePodSetAssignment("scale-down").Assignment(corev1.ResourceCPU, "default", "1").Count(3).Obj(),
							utiltestingapi.MakePodSetAssignment("stay-the-same").Assignment(corev1.ResourceCPU, "default", "1").Count(3).Obj(),
						).
						Obj(), now).
					Obj(),
			},
		},
		"OneWorkloadSlice_UnreservedQuota_ScaleUp": {
			args: args{
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).Build(),
				jobPodSets:   []kueue.PodSet{*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				workload: utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("2").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
		},
		"OneWorkloadSlice_UnreservedQuota_ScaleDown": {
			args: args{
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).Build(),
				jobPodSets:   []kueue.PodSet{*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				workload: utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("2").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
		},
		"OneWorkloadSlice_UpdateFailure": {
			args: args{
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).WithInterceptorFuncs(interceptor.Funcs{
					Update: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						return errTest
					}}).Build(),
				jobPodSets:   []kueue.PodSet{*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				error:      errTest,
				compatible: true,
			},
		},
		//
		"TwoWorkloads_BothUnreserved_NewIsCurrent": {
			args: args{
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(fiveMinutesAgo).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						Obj(),
					utiltestingapi.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(now).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).
					Build(),
				jobPodSets:   []kueue.PodSet{*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				workload: utiltestingapi.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("1").
					Creation(now).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
				finishedWorkloads: map[string]string{
					testJobObject.Name + "-1": kueue.WorkloadFinishedReasonOutOfSync,
				},
			},
		},
		"TwoWorkloads_BothUnreserved_NewIsCurrent_FailureToPatchOldSliceStatus": {
			args: args{
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(fiveMinutesAgo).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						Obj(),
					utiltestingapi.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(now).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).
					WithInterceptorFuncs(interceptor.Funcs{
						SubResourceApply: func(ctx context.Context, client client.Client, subResourceName string, applyConf runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
							return errTest
						},
					}).
					Build(),
				jobPodSets:   []kueue.PodSet{*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				error:      errTest,
				compatible: true,
			},
		},
		"TwoWorkloads_NewIsIncompatible": {
			args: args{
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(fiveMinutesAgo).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuotaAt(utiltestingapi.MakeAdmission("default").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
						Obj(),
					utiltestingapi.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(now).
						Annotation(WorkloadSliceReplacementFor, string(workload.Key(utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).Obj()))).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).
					Build(),
				jobPodSets:   []kueue.PodSet{*utiltestingapi.MakePodSet("different-key", 1).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
		},
		"TwoWorkloads_BothWithReservedQuota": {
			args: args{
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(fiveMinutesAgo).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuotaAt(utiltestingapi.MakeAdmission("default").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
						Obj(),
					utiltestingapi.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(now).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuotaAt(utiltestingapi.MakeAdmission("default").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").Count(3).Obj()).Obj(), now).
						Obj()).
					Build(),
				jobPodSets:   []kueue.PodSet{*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				finishedWorkloads: map[string]string{
					testJobObject.Name + "-1": kueue.WorkloadFinishedReasonOutOfSync,
				},
			},
		},
		"TwoWorkloads_OldWithReservedQuotaAndEvicted_NewWithoutQuotaReservation": {
			args: args{
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(fiveMinutesAgo).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuotaAt(utiltestingapi.MakeAdmission("default").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
						EvictedAt(now).
						Obj(),
					utiltestingapi.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(now).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).
					Build(),
				jobPodSets:   []kueue.PodSet{*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				workload: utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("1").Creation(fiveMinutesAgo).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("default").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					EvictedAt(now).Obj(),
			},
		},
		"TwoWorkloadSlices_NewIsUnreservedAndCurrent": {
			args: args{
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(fiveMinutesAgo).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuotaAt(utiltestingapi.MakeAdmission("default").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
						Obj(),
					utiltestingapi.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(now).
						Annotation(WorkloadSliceReplacementFor, string(workload.Key(utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).Obj()))).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).Build(),
				jobPodSets:   []kueue.PodSet{*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				workload: utiltestingapi.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("1").
					Creation(now).
					Annotation(WorkloadSliceReplacementFor, string(workload.Key(utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).Obj()))).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
		},
		"TwoWorkloadSlices_NewIsUnreservedAndOutOfSync_ScaleUp": {
			args: args{
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(fiveMinutesAgo).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuotaAt(utiltestingapi.MakeAdmission("default").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
						Obj(),
					utiltestingapi.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(now).
						Annotation(WorkloadSliceReplacementFor, string(workload.Key(utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).Obj()))).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).
					Build(),
				jobPodSets:   []kueue.PodSet{*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				workload: utiltestingapi.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("2").
					Creation(now).
					Annotation(WorkloadSliceReplacementFor, string(workload.Key(utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).Obj()))).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
		},
		"TwoWorkloadSlices_NewIsUnreservedAndOutOfSync_ScaleDown": {
			args: args{
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(fiveMinutesAgo).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuotaAt(utiltestingapi.MakeAdmission("default").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
						Obj(),
					utiltestingapi.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(now).
						Annotation(WorkloadSliceReplacementFor, string(workload.Key(utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).Obj()))).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).
					Build(),
				jobPodSets:   []kueue.PodSet{*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				workload: utiltestingapi.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("2").
					Creation(now).
					Annotation(WorkloadSliceReplacementFor, string(workload.Key(utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).Obj()))).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
		},
		"TwoWorkloadSlices_NewIsUnreservedAndOutOfSync_UpdateFailure": {
			args: args{
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(fiveMinutesAgo).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuotaAt(utiltestingapi.MakeAdmission("default").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
						Obj(),
					utiltestingapi.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(now).
						Annotation(WorkloadSliceReplacementFor, string(workload.Key(utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).Obj()))).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						Obj()).
					WithInterceptorFuncs(interceptor.Funcs{
						Update: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.UpdateOption) error {
							// Assert that we are updating correct workload slice.
							if obj.GetName() != testJobObject.Name+"-2" {
								t.Errorf("unexptected workload update: %v", obj)
							}
							return errTest
						},
					}).
					Build(),
				jobPodSets:   []kueue.PodSet{*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				error:      errTest,
				compatible: true,
			},
		},
		"TwoWorkloads_OldWithReservedQuota_NewWithReplacementAnnotationAndQuota": {
			args: args{
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(fiveMinutesAgo).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuotaAt(utiltestingapi.MakeAdmission("default").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
						Obj(),
					utiltestingapi.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(now).
						Annotation(WorkloadSliceReplacementFor, string(workload.NewReference(testJobObject.Namespace, testJobObject.Name+"-1"))).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
						ReserveQuotaAt(utiltestingapi.MakeAdmission("default").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").Count(3).Obj()).Obj(), now).
						Obj()).
					Build(),
				jobPodSets:   []kueue.PodSet{*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				workload: utiltestingapi.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("1").
					Creation(now).
					Annotation(WorkloadSliceReplacementFor, string(workload.NewReference(testJobObject.Namespace, testJobObject.Name+"-1"))).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("default").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").Count(3).Obj()).Obj(), now).
					Obj(),
				finishedWorkloads: map[string]string{
					testJobObject.Name + "-1": kueue.WorkloadSliceReplaced,
				},
			},
		},
		//
		"MoreThanTwoWorkloadSlices": {
			args: args{
				clnt: testWorkloadClientBuilder().WithObjects(
					testWorkload.Clone().
						Name(testJobObject.Name+"-1").
						ResourceVersion("100").
						Creation(fiveMinutesAgo).
						PodSets(kueue.PodSet{Name: kueue.DefaultPodSetName, Count: 1}).
						Obj(),
					testWorkload.Clone().
						Name(testJobObject.Name+"-2").
						ResourceVersion("101").
						Creation(fiveMinutesAgo.Add(time.Second)).
						PodSets(kueue.PodSet{Name: kueue.DefaultPodSetName, Count: 2}).
						Obj(),
					testWorkload.Clone().
						Name(testJobObject.Name+"-3").
						ResourceVersion("102").
						Creation(fiveMinutesAgo.Add(2*time.Second)).
						PodSets(kueue.PodSet{Name: kueue.DefaultPodSetName, Count: 3}).
						Obj()).
					Build(),
				jobPodSets:   []kueue.PodSet{{Name: kueue.DefaultPodSetName, Count: 3}},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				workload: testWorkload.Clone().
					Name(testJobObject.Name + "-3").
					ResourceVersion("102").
					Creation(fiveMinutesAgo.Add(2 * time.Second)).
					PodSets(kueue.PodSet{Name: kueue.DefaultPodSetName, Count: 3}).
					Obj(),
				finishedWorkloads: map[string]string{
					testJobObject.Name + "-1": kueue.WorkloadFinishedReasonOutOfSync,
					testJobObject.Name + "-2": kueue.WorkloadFinishedReasonOutOfSync,
				},
			},
		},
		// The origin still reserves quota while eviction is pending, so it is returned
		// to the job reconciler until an admitted replacement takes over its Pods.
		"EvictedOriginWithReservedReplacement_ReplacementNotAdmitted": {
			args: args{
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(fiveMinutesAgo).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						SimpleReserveQuota("default", "default", now).AdmittedAt(true, now).EvictedAt(now).
						Obj(),
					utiltestingapi.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(now).
						Annotation(WorkloadSliceReplacementFor, string(workload.Key(utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).Obj()))).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
						SimpleReserveQuota("default", "default", now).AdmittedAt(false, now).
						Obj()).
					Build(),
				jobPodSets:   []kueue.PodSet{*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				workload: utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("1").
					Creation(fiveMinutesAgo).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					SimpleReserveQuota("default", "default", now).AdmittedAt(true, now).EvictedAt(now).
					Obj(),
			},
		},
		// Once the replacement is admitted, it takes over the origin's Pods, so the
		// origin is finished with a "SliceReplaced" reason and the replacement is selected.
		"EvictedOriginWithReservedReplacement_ReplacementAdmitted": {
			args: args{
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(fiveMinutesAgo).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						SimpleReserveQuota("default", "default", now).AdmittedAt(true, now).EvictedAt(now).
						Obj(),
					utiltestingapi.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(now).
						Annotation(WorkloadSliceReplacementFor, string(workload.Key(utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).Obj()))).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
						SimpleReserveQuota("default", "default", now).AdmittedAt(true, now).
						Obj()).
					Build(),
				jobPodSets:   []kueue.PodSet{*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				workload: utiltestingapi.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("1").
					Creation(now).
					Annotation(WorkloadSliceReplacementFor, string(workload.Key(utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).Obj()))).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
					SimpleReserveQuota("default", "default", now).AdmittedAt(true, now).
					Obj(),
				finishedWorkloads: map[string]string{
					testJobObject.Name + "-1": kueue.WorkloadSliceReplaced,
				},
			},
		},
		// An admitted replacement that was itself evicted no longer owns the origin's Pods,
		// so it must not replace the origin: the origin is returned and stays unfinished.
		"EvictedOriginWithReservedReplacement_ReplacementAdmittedAndEvicted": {
			args: args{
				clnt: testWorkloadClientBuilder().WithObjects(
					utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(fiveMinutesAgo).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
						SimpleReserveQuota("default", "default", now).AdmittedAt(true, now).EvictedAt(now).
						Obj(),
					utiltestingapi.MakeWorkload(testJobObject.Name+"-2", testJobObject.Namespace).
						OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
						ResourceVersion("1").
						Creation(now).
						Annotation(WorkloadSliceReplacementFor, string(workload.Key(utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).Obj()))).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
						SimpleReserveQuota("default", "default", now).AdmittedAt(true, now).EvictedAt(now).
						Obj()).
					Build(),
				jobPodSets:   []kueue.PodSet{*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()},
				jobObject:    testJobObject,
				jobObjectGVK: testJobGVK,
			},
			want: want{
				compatible: true,
				workload: utiltestingapi.MakeWorkload(testJobObject.Name+"-1", testJobObject.Namespace).
					OwnerReference(testJobGVK, testJobObject.Name, string(testJobObject.UID)).
					ResourceVersion("1").
					Creation(fiveMinutesAgo).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					SimpleReserveQuota("default", "default", now).AdmittedAt(true, now).EvictedAt(now).
					Obj(),
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			gotWorkload, gotCompatible, gotError := EnsureWorkloadSlices(ctx, tt.args.clnt, fakeClock, tt.args.jobPodSets, tt.args.jobObject, tt.args.jobObjectGVK)
			if diff := cmp.Diff(tt.want.error, gotError, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("EnsureWorkloadSlices() error (-want,+got):\n%s", diff)
				return
			}
			if diff := cmp.Diff(tt.want.workload, gotWorkload, cmpopts.EquateApproxTime(time.Second)); diff != "" {
				t.Errorf("EnsureWorkloadSlices() (-want,+got):\n%s", diff)
			}
			if gotCompatible != tt.want.compatible {
				t.Errorf("EnsureWorkloadSlices() compatible = %v, want %v", gotCompatible, tt.want.compatible)
			}
			var workloads kueue.WorkloadList
			if err := tt.args.clnt.List(ctx, &workloads); err != nil {
				t.Fatalf("Failed to list workloads: %v", err)
			}
			gotFinished := make(map[string]string)
			for _, wl := range workloads.Items {
				if cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadFinished); cond != nil && cond.Status == metav1.ConditionTrue {
					gotFinished[wl.Name] = cond.Reason
				}
			}
			if diff := cmp.Diff(tt.want.finishedWorkloads, gotFinished, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("EnsureWorkloadSlices() finished workloads (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestNormalizeActiveSlices(t *testing.T) {
	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)

	admitted := func(w *utiltestingapi.WorkloadWrapper) *utiltestingapi.WorkloadWrapper {
		return w.ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
			utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").Obj(),
		).Obj(), now)
	}

	type want struct {
		survivor     string
		keptAdmitted string
		error        error
	}

	tests := map[string]struct {
		partialScaleUp bool
		workloads      []kueue.Workload
		want           want
	}{
		"two admitted, keep newest": {
			workloads: []kueue.Workload{
				*admitted(utiltestingapi.MakeWorkload("wl-a", "ns").ResourceVersion("1").Creation(now.Add(-time.Minute)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj())).Obj(),
				*admitted(utiltestingapi.MakeWorkload("wl-b", "ns").ResourceVersion("1").Creation(now).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj())).Obj(),
			},
			want: want{survivor: "wl-b"},
		},
		"admitted + pending replacement, keep replacement": {
			workloads: []kueue.Workload{
				*admitted(utiltestingapi.MakeWorkload("wl-a", "ns").ResourceVersion("1").Creation(now.Add(-time.Minute)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj())).Obj(),
				*utiltestingapi.MakeWorkload("wl-b", "ns").ResourceVersion("1").Creation(now).
					Annotation(WorkloadSliceReplacementFor, "ns/wl-a").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).Obj(),
			},
			want: want{survivor: "wl-b", keptAdmitted: "wl-a"},
		},
		"evicted admitted + pending, keep pending and finish evicted": {
			workloads: []kueue.Workload{
				*admitted(utiltestingapi.MakeWorkload("wl-a", "ns").ResourceVersion("1").Creation(now.Add(-time.Minute)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj())).
					EvictedAt(now).Obj(),
				*utiltestingapi.MakeWorkload("wl-b", "ns").ResourceVersion("1").Creation(now).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).Obj(),
			},
			want: want{survivor: "wl-b"},
		},
		// wl-b is a partial scale-up probe (it carries a minCount) replacing the
		// evicted wl-a. It's treated like any other pending replacement and kept.
		"evicted admitted with pending probe, keep probe and finish evicted": {
			partialScaleUp: true,
			workloads: []kueue.Workload{
				*admitted(utiltestingapi.MakeWorkload("wl-a", "ns").ResourceVersion("1").Creation(now.Add(-time.Minute)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj())).
					EvictedAt(now).Obj(),
				*utiltestingapi.MakeWorkload("wl-b", "ns").ResourceVersion("1").Creation(now).
					Annotation(WorkloadSliceReplacementFor, "ns/wl-a").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").SetMinimumCount(2).Obj()).Obj(),
			},
			want: want{survivor: "wl-b"},
		},
		// Same shape as above, but without the feature enabled: minCount could only
		// have come from classic PartialAdmission here. Same outcome either way.
		"evicted admitted with minCount but feature disabled, keep pending and finish evicted": {
			workloads: []kueue.Workload{
				*admitted(utiltestingapi.MakeWorkload("wl-a", "ns").ResourceVersion("1").Creation(now.Add(-time.Minute)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj())).
					EvictedAt(now).Obj(),
				*utiltestingapi.MakeWorkload("wl-b", "ns").ResourceVersion("1").Creation(now).
					Annotation(WorkloadSliceReplacementFor, "ns/wl-a").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").SetMinimumCount(2).Obj()).Obj(),
			},
			want: want{survivor: "wl-b"},
		},
		"forked DAG, both replace same finished target, keep newest": {
			workloads: []kueue.Workload{
				*admitted(utiltestingapi.MakeWorkload("wl-a", "ns").ResourceVersion("1").Creation(now.Add(-time.Minute)).
					Annotation(WorkloadSliceReplacementFor, "ns/wl-old").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj())).Obj(),
				*admitted(utiltestingapi.MakeWorkload("wl-b", "ns").ResourceVersion("1").Creation(now).
					Annotation(WorkloadSliceReplacementFor, "ns/wl-old").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj())).Obj(),
			},
			want: want{survivor: "wl-b"},
		},
		"all non-admitted non-evicted, keep newest": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-a", "ns").ResourceVersion("1").Creation(now.Add(-time.Minute)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).Obj(),
				*utiltestingapi.MakeWorkload("wl-b", "ns").ResourceVersion("1").Creation(now).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).Obj(),
			},
			want: want{survivor: "wl-b"},
		},
		"all evicted, survivor is nil": {
			workloads: []kueue.Workload{
				*admitted(utiltestingapi.MakeWorkload("wl-a", "ns").ResourceVersion("1").Creation(now.Add(-time.Minute)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj())).
					EvictedAt(now).Obj(),
				*admitted(utiltestingapi.MakeWorkload("wl-b", "ns").ResourceVersion("1").Creation(now).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj())).
					EvictedAt(now).Obj(),
			},
			want: want{survivor: ""},
		},
		"admitted + pending without replacement annotation, keep admitted": {
			workloads: []kueue.Workload{
				*admitted(utiltestingapi.MakeWorkload("wl-a", "ns").ResourceVersion("1").Creation(now.Add(-time.Minute)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj())).Obj(),
				*utiltestingapi.MakeWorkload("wl-b", "ns").ResourceVersion("1").Creation(now).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).Obj(),
			},
			want: want{survivor: "wl-a"},
		},
		"three workloads, admitted + two pending, keep replacement targeting admitted": {
			workloads: []kueue.Workload{
				*admitted(utiltestingapi.MakeWorkload("wl-a", "ns").ResourceVersion("1").Creation(now.Add(-2 * time.Minute)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj())).Obj(),
				*utiltestingapi.MakeWorkload("wl-b", "ns").ResourceVersion("1").Creation(now.Add(-time.Minute)).
					Annotation(WorkloadSliceReplacementFor, "ns/wl-old").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).Obj(),
				*utiltestingapi.MakeWorkload("wl-c", "ns").ResourceVersion("1").Creation(now).
					Annotation(WorkloadSliceReplacementFor, "ns/wl-a").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).Obj(),
			},
			want: want{survivor: "wl-c", keptAdmitted: "wl-a"},
		},
		"four same-second slices, chained replacements, admitted slice survives": {
			workloads: []kueue.Workload{
				*admitted(utiltestingapi.MakeWorkload("wl-a", "ns").ResourceVersion("1").Creation(now).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj())).Obj(),
				*utiltestingapi.MakeWorkload("wl-b", "ns").ResourceVersion("1").Creation(now).
					Annotation(WorkloadSliceReplacementFor, "ns/wl-a").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).Obj(),
				*admitted(utiltestingapi.MakeWorkload("wl-c", "ns").ResourceVersion("1").Creation(now).
					Annotation(WorkloadSliceReplacementFor, "ns/wl-a").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj())).Obj(),
				*utiltestingapi.MakeWorkload("wl-d", "ns").ResourceVersion("1").Creation(now).
					Annotation(WorkloadSliceReplacementFor, "ns/wl-c").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).Request(corev1.ResourceCPU, "1").Obj()).Obj(),
			},
			want: want{survivor: "wl-d", keptAdmitted: "wl-c"},
		},
		"forked claim in adversarial order, admitted fork wins over pending": {
			workloads: []kueue.Workload{
				// Adversarial iteration order: wl-c before wl-b before wl-a.
				// wl-b and wl-c both claim to replace wl-a (forked chain from a race).
				// wl-c is admitted and has a pending replacement wl-d.
				*admitted(utiltestingapi.MakeWorkload("wl-c", "ns").ResourceVersion("1").Creation(now).
					Annotation(WorkloadSliceReplacementFor, "ns/wl-a").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj())).Obj(),
				*utiltestingapi.MakeWorkload("wl-b", "ns").ResourceVersion("1").Creation(now).
					Annotation(WorkloadSliceReplacementFor, "ns/wl-a").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).Obj(),
				*admitted(utiltestingapi.MakeWorkload("wl-a", "ns").ResourceVersion("1").Creation(now).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj())).Obj(),
				*utiltestingapi.MakeWorkload("wl-d", "ns").ResourceVersion("1").Creation(now).
					Annotation(WorkloadSliceReplacementFor, "ns/wl-c").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).Request(corev1.ResourceCPU, "1").Obj()).Obj(),
			},
			want: want{survivor: "wl-d", keptAdmitted: "wl-c"},
		},
		// Neither holds a reservation, so the survivor is the latest one. The
		// caller passes these already sorted with a UID tie-break, so the last
		// is the latest even when both were created in the same second.
		"two pending slices created in the same second": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-a", "ns").ResourceVersion("1").Creation(now).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).Obj(),
				*utiltestingapi.MakeWorkload("wl-b", "ns").ResourceVersion("1").Creation(now).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).Obj(),
			},
			want: want{survivor: "wl-b"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp: tc.partialScaleUp,
			})
			ctx, _ := utiltesting.ContextWithLog(t)
			testSchema := runtime.NewScheme()
			_ = kueue.AddToScheme(testSchema)
			var objs []client.Object
			for i := range tc.workloads {
				objs = append(objs, &tc.workloads[i])
			}
			clnt := fake.NewClientBuilder().
				WithScheme(testSchema).
				WithStatusSubresource(&kueue.Workload{}).
				WithObjects(objs...).
				Build()

			survivor, err := normalizeActiveSlices(ctx, clnt, fakeClock, tc.workloads)
			if diff := cmp.Diff(tc.want.error, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("normalizeActiveSlices() error (-want,+got):\n%s", diff)
			}
			gotName := ""
			if survivor != nil {
				gotName = survivor.Name
			}
			if gotName != tc.want.survivor {
				t.Errorf("normalizeActiveSlices() survivor = %q, want %q", gotName, tc.want.survivor)
			}

			for i := range tc.workloads {
				wl := &kueue.Workload{}
				if err := clnt.Get(ctx, client.ObjectKeyFromObject(&tc.workloads[i]), wl); err != nil {
					t.Fatalf("failed to get workload %s: %v", tc.workloads[i].Name, err)
				}
				kept := wl.Name == tc.want.survivor || wl.Name == tc.want.keptAdmitted
				if !kept && !workloadfinish.IsFinished(wl) {
					t.Errorf("workload %q should be finished but is not", wl.Name)
				}
			}
		})
	}
}

func TestLowerProbeFloor(t *testing.T) {
	const otherPodSet kueue.PodSetReference = "other"

	tests := map[string]struct {
		probeMinCount       int32
		predecessorMinCount *int32
		predecessorPodSet   kueue.PodSetReference
		wantMinCount        int32
		wantUpdated         bool
	}{
		"lowers when predecessor's floor is lower": {
			probeMinCount:       6,
			predecessorMinCount: new(int32(5)),
			predecessorPodSet:   kueue.DefaultPodSetName,
			wantMinCount:        5,
			wantUpdated:         true,
		},
		"never raises when predecessor's floor is higher": {
			probeMinCount:       6,
			predecessorMinCount: new(int32(7)),
			predecessorPodSet:   kueue.DefaultPodSetName,
			wantMinCount:        6,
			wantUpdated:         false,
		},
		"skips when predecessor has no floor at all": {
			probeMinCount:       6,
			predecessorMinCount: nil,
			predecessorPodSet:   kueue.DefaultPodSetName,
			wantMinCount:        6,
			wantUpdated:         false,
		},
		"skips a PodSet name the predecessor doesn't have": {
			probeMinCount:       6,
			predecessorMinCount: new(int32(5)),
			predecessorPodSet:   otherPodSet,
			wantMinCount:        6,
			wantUpdated:         false,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			testSchema := runtime.NewScheme()
			_ = kueue.AddToScheme(testSchema)

			probe := utiltestingapi.MakeWorkload("probe", "ns").ResourceVersion("1").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(tc.probeMinCount).Obj()).Obj()
			predecessorPodSet := utiltestingapi.MakePodSet(tc.predecessorPodSet, 6)
			if tc.predecessorMinCount != nil {
				predecessorPodSet = predecessorPodSet.SetMinimumCount(*tc.predecessorMinCount)
			}
			predecessor := utiltestingapi.MakeWorkload("predecessor", "ns").
				PodSets(*predecessorPodSet.Obj()).Obj()

			clnt := fake.NewClientBuilder().WithScheme(testSchema).WithObjects(probe).Build()

			if err := lowerProbeMinCount(ctx, clnt, probe, predecessor); err != nil {
				t.Fatalf("lowerProbeMinCount() error = %v", err)
			}

			if got := *probe.Spec.PodSets[0].MinCount; got != tc.wantMinCount {
				t.Errorf("probe.Spec.PodSets[0].MinCount = %d, want %d", got, tc.wantMinCount)
			}

			persisted := &kueue.Workload{}
			if err := clnt.Get(ctx, client.ObjectKeyFromObject(probe), persisted); err != nil {
				t.Fatalf("failed to get probe: %v", err)
			}
			gotPersisted := *persisted.Spec.PodSets[0].MinCount
			wantPersisted := tc.probeMinCount
			if tc.wantUpdated {
				wantPersisted = tc.wantMinCount
			}
			if gotPersisted != wantPersisted {
				t.Errorf("persisted MinCount = %d, want %d (wantUpdated=%v)", gotPersisted, wantPersisted, tc.wantUpdated)
			}
		})
	}
}

func TestReplacedWorkloadSlice(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)
	type args struct {
		wl   *workload.Info
		snap *schdcache.Snapshot
	}
	type want struct {
		wl      *workload.Info
		targets []*preemption.Target
	}

	tests := map[string]struct {
		featureGates map[featuregate.Feature]bool
		args         args
		want         want
	}{
		"FeatureNotEnabled": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: false},
		},
		"EdgeCase_WorkloadIsNil": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
		},
		"EdgeCase_SnapshotIsNil": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			args: args{
				wl: workload.NewInfo(log, utiltestingapi.MakeWorkload("test", "default").Obj()),
			},
		},
		"WorkloadWithoutReplacementAnnotation": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			args: args{
				wl:   workload.NewInfo(log, utiltestingapi.MakeWorkload("test", "default").Obj()),
				snap: &schdcache.Snapshot{},
			},
		},
		"ReplacedWorkloadIsNotFound_MissingClusterQueue": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			args: args{
				wl: workload.NewInfo(log, utiltestingapi.MakeWorkload("test-new", "default").
					Annotation(WorkloadSliceReplacementFor, "test-old").
					Obj()),
				snap: &schdcache.Snapshot{
					Manager: hierarchy.NewManagerForTest(
						map[kueue.CohortReference]*schdcache.CohortSnapshot{},
						map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{}),
				},
			},
		},
		"EdgeCase_ReplacedWorkloadIsNotFound_NotInClusterQueue": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			args: args{
				wl: workload.NewInfo(log, utiltestingapi.MakeWorkload("test-new", "default").
					Annotation(WorkloadSliceReplacementFor, "test-old").
					Admission(utiltestingapi.MakeAdmission("default").Obj()).
					Obj()),
				snap: &schdcache.Snapshot{
					Manager: hierarchy.NewManagerForTest(
						map[kueue.CohortReference]*schdcache.CohortSnapshot{},
						map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
							"default": {},
						}),
				},
			},
		},
		"ReplacedWorkloadIsFound": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			args: args{
				wl: workload.NewInfo(log, utiltestingapi.MakeWorkload("test-new", "default").
					Annotation(WorkloadSliceReplacementFor, "test-old").
					Admission(utiltestingapi.MakeAdmission("default").Obj()).
					Obj()),
				snap: &schdcache.Snapshot{
					Manager: hierarchy.NewManagerForTest(
						map[kueue.CohortReference]*schdcache.CohortSnapshot{},
						map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
							"default": {
								Workloads: map[workload.Reference]*workload.Info{
									"test-old": workload.NewInfo(log, utiltestingapi.MakeWorkload("test-old", "default").Obj()),
								},
							},
						}),
				},
			},
			want: want{
				wl: workload.NewInfo(log, utiltestingapi.MakeWorkload("test-old", "default").Obj()),
				targets: []*preemption.Target{
					{WorkloadInfo: workload.NewInfo(log, utiltestingapi.MakeWorkload("test-old", "default").Obj())},
				},
			},
		},
		"CrossNamespaceReplacementIsRejected": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			args: args{
				wl: workload.NewInfo(log, utiltestingapi.MakeWorkload("test-new", "other-ns").
					Annotation(WorkloadSliceReplacementFor, "default/test-old").
					Admission(utiltestingapi.MakeAdmission("shared-cq").Obj()).
					Obj()),
				snap: &schdcache.Snapshot{
					Manager: hierarchy.NewManagerForTest(
						map[kueue.CohortReference]*schdcache.CohortSnapshot{},
						map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
							"shared-cq": {
								Workloads: map[workload.Reference]*workload.Info{
									"default/test-old": workload.NewInfo(log, utiltestingapi.MakeWorkload("test-old", "default").Obj()),
								},
							},
						},
					),
				},
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tt.featureGates)
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

func TestFindLatestActiveWorkload(t *testing.T) {
	now := time.Now()
	admission := utiltestingapi.MakeAdmission("cq").Obj()

	live := func(name string, created time.Time) *utiltestingapi.WorkloadWrapper {
		return testWorkload(name, testJobObject.Name, testJobObject.UID, created).
			ReserveQuotaAt(admission, created)
	}

	cases := map[string]struct {
		workloads []kueue.Workload
		want      string
	}{
		"none": {},
		"the only reserved one": {
			workloads: []kueue.Workload{*live("only", now).Obj()},
			want:      "only",
		},
		"the newest reserved one": {
			workloads: []kueue.Workload{
				*live("older", now.Add(-time.Minute)).Obj(),
				*live("newer", now).Obj(),
			},
			want: "newer",
		},
		// A pending AdmissionCheck does not disqualify a slice from being the
		// chain's active one: FindLatestActiveWorkload only tracks quota
		// reservation. Callers that must wait for full admission apply their
		// own additional check on the result.
		"quota reservation is active even with a pending admission check": {
			workloads: []kueue.Workload{
				*live("reserved", now).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:  "provisioning",
						State: kueue.CheckStatePending,
					}).
					Obj(),
			},
			want: "reserved",
		},
		// Eviction sets its condition before the reservation is released, so a
		// slice can still report one while its capacity is on the way out. The
		// older slice is the one still holding capacity.
		"a newer slice that is being evicted": {
			workloads: []kueue.Workload{
				*live("older", now.Add(-time.Minute)).Obj(),
				*live("evicting", now).EvictedAt(now).Obj(),
			},
			want: "older",
		},
		"every slice is being evicted": {
			workloads: []kueue.Workload{
				*live("one", now.Add(-time.Minute)).EvictedAt(now).Obj(),
				*live("two", now).EvictedAt(now).Obj(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cl := testWorkloadClientBuilder().
				WithLists(&kueue.WorkloadList{Items: tc.workloads}).Build()

			got, err := FindLatestActiveWorkload(ctx, cl, testJobObject, testJobGVK)
			if err != nil {
				t.Fatalf("FindLatestActiveWorkload() error = %v", err)
			}
			var gotName string
			if got != nil {
				gotName = got.Name
			}
			if gotName != tc.want {
				t.Errorf("FindLatestActiveWorkload() = %q, want %q", gotName, tc.want)
			}
		})
	}
}
