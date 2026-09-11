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

// Package indexer_test is an external test package so it can use the
// structured object wrappers from pkg/util/testing without an import cycle
// (those packages transitively import pkg/controller/core/indexer).
package indexer_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

var batchJobGVK = schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}

func TestOwnerReferenceIndexKey(t *testing.T) {
	cases := map[string]struct {
		gvk  schema.GroupVersionKind
		want string
	}{
		"batch/v1 Job": {
			gvk:  batchJobGVK,
			want: ".metadata.ownerReferences[batch.Job]",
		},
		"core group (empty group)": {
			gvk:  schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"},
			want: ".metadata.ownerReferences[.Pod]",
		},
		"custom resource": {
			gvk:  schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
			want: ".metadata.ownerReferences[apps.Deployment]",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := indexer.OwnerReferenceIndexKey(tc.gvk)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOwnerReferenceIndexFieldMatcher(t *testing.T) {
	got := indexer.OwnerReferenceIndexFieldMatcher(batchJobGVK, "my-job")

	want := client.MatchingFields{".metadata.ownerReferences[batch.Job]": "my-job"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("MatchingFields mismatch (-want +got):\n%s", diff)
	}
}

func TestWorkloadOwnerIndexFunc(t *testing.T) {
	indexFn := indexer.WorkloadOwnerIndexFunc(batchJobGVK)

	cases := map[string]struct {
		obj  client.Object
		want []string
	}{
		"non-workload object returns nil": {
			obj:  utiltesting.MakeLimitRange("lr", "ns").Obj(),
			want: nil,
		},
		"workload with no owner references returns nil": {
			obj:  utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			want: nil,
		},
		"workload with non-matching kind is skipped": {
			obj: utiltestingapi.MakeWorkload("wl", "ns").
				OwnerReference(schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "CronJob"}, "cron", "").
				Obj(),
			want: nil,
		},
		"workload with non-matching apiVersion is skipped": {
			obj: utiltestingapi.MakeWorkload("wl", "ns").
				OwnerReference(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Job"}, "job", "").
				Obj(),
			want: nil,
		},
		"workload with single matching owner": {
			obj: utiltestingapi.MakeWorkload("wl", "ns").
				OwnerReference(batchJobGVK, "my-job", "").
				Obj(),
			want: []string{"my-job"},
		},
		"workload with multiple owners, only matching ones returned": {
			obj: utiltestingapi.MakeWorkload("wl", "ns").
				OwnerReference(batchJobGVK, "job-1", "").
				OwnerReference(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, "deploy", "").
				OwnerReference(batchJobGVK, "job-2", "").
				Obj(),
			want: []string{"job-1", "job-2"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := indexFn(tc.obj)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("index result mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIndexQueueClusterQueue(t *testing.T) {
	cases := map[string]struct {
		obj  client.Object
		want []string
	}{
		"non-LocalQueue returns nil": {
			obj:  utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			want: nil,
		},
		"LocalQueue returns its clusterQueue name": {
			obj:  utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("my-cq").Obj(),
			want: []string{"my-cq"},
		},
		"LocalQueue with empty clusterQueue": {
			obj:  utiltestingapi.MakeLocalQueue("lq", "ns").Obj(),
			want: []string{""},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := indexer.IndexQueueClusterQueue(tc.obj)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIndexWorkloadQueue(t *testing.T) {
	cases := map[string]struct {
		obj  client.Object
		want []string
	}{
		"non-Workload returns nil": {
			obj:  utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			want: nil,
		},
		"workload returns its queue name": {
			obj:  utiltestingapi.MakeWorkload("wl", "ns").Queue("user-queue").Obj(),
			want: []string{"user-queue"},
		},
		"workload with empty queue name": {
			obj:  utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			want: []string{""},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := indexer.IndexWorkloadQueue(tc.obj)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIndexWorkloadClusterQueue(t *testing.T) {
	cases := map[string]struct {
		obj  client.Object
		want []string
	}{
		"non-Workload returns nil": {
			obj:  utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			want: nil,
		},
		"workload without admission returns nil": {
			obj:  utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			want: nil,
		},
		"workload with admission returns cluster queue": {
			obj: utiltestingapi.MakeWorkload("wl", "ns").
				Admission(utiltestingapi.MakeAdmission("my-cq").Obj()).
				Obj(),
			want: []string{"my-cq"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := indexer.IndexWorkloadClusterQueue(tc.obj)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIndexLimitRangeHasContainerOrPodType(t *testing.T) {
	cases := map[string]struct {
		obj  client.Object
		want []string
	}{
		"non-LimitRange returns nil": {
			obj:  utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			want: nil,
		},
		"LimitRange with no limits returns nil": {
			obj:  utiltesting.MakeLimitRange("lr", "ns").LimitTypes().Obj(),
			want: nil,
		},
		"LimitRange with only Pod type returns nil": {
			obj:  utiltesting.MakeLimitRange("lr", "ns").LimitTypes(corev1.LimitTypePod).Obj(),
			want: []string{"true"},
		},
		"LimitRange with Container type returns true": {
			obj:  utiltesting.MakeLimitRange("lr", "ns").LimitTypes(corev1.LimitTypeContainer).Obj(),
			want: []string{"true"},
		},
		"LimitRange with both Pod and Container types returns true": {
			obj:  utiltesting.MakeLimitRange("lr", "ns").LimitTypes(corev1.LimitTypePod, corev1.LimitTypeContainer).Obj(),
			want: []string{"true"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := indexer.IndexLimitRangeHasContainerOrPodType(tc.obj)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIndexWorkloadQuotaReserved(t *testing.T) {
	cases := map[string]struct {
		obj  client.Object
		want []string
	}{
		"non-Workload returns nil": {
			obj:  utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			want: nil,
		},
		"workload without QuotaReserved condition returns False": {
			obj:  utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			want: []string{"False"},
		},
		"workload with QuotaReserved=True": {
			obj: utiltestingapi.MakeWorkload("wl", "ns").
				Condition(metav1.Condition{Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionTrue}).
				Obj(),
			want: []string{"True"},
		},
		"workload with QuotaReserved=False": {
			obj: utiltestingapi.MakeWorkload("wl", "ns").
				Condition(metav1.Condition{Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionFalse}).
				Obj(),
			want: []string{"False"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := indexer.IndexWorkloadQuotaReserved(tc.obj)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIndexWorkloadRuntimeClass(t *testing.T) {
	cases := map[string]struct {
		obj  client.Object
		want []string
	}{
		"non-Workload returns nil": {
			obj:  utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			want: nil,
		},
		"workload with no podsets returns nil": {
			obj:  utiltestingapi.MakeWorkload("wl", "ns").PodSets().Obj(),
			want: nil,
		},
		"podset with no runtime class returns nil": {
			obj: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			want: nil,
		},
		"workload with single runtime class": {
			obj: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).RuntimeClass("rc-fast").Obj()).
				Obj(),
			want: []string{"rc-fast"},
		},
		"workload with multiple distinct runtime classes": {
			obj: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 1).RuntimeClass("rc-fast").Obj(),
					*utiltestingapi.MakePodSet("ps2", 1).RuntimeClass("rc-slow").Obj(),
				).
				Obj(),
			want: []string{"rc-fast", "rc-slow"},
		},
		"duplicate runtime class across podsets is deduplicated": {
			obj: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 1).RuntimeClass("rc-fast").Obj(),
					*utiltestingapi.MakePodSet("ps2", 1).RuntimeClass("rc-fast").Obj(),
				).
				Obj(),
			want: []string{"rc-fast"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := indexer.IndexWorkloadRuntimeClass(tc.obj)
			if diff := cmp.Diff(tc.want, got,
				cmpopts.SortSlices(func(a, b string) bool { return a < b }),
				cmpopts.EquateEmpty(),
			); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIndexOwnerUID(t *testing.T) {
	cases := map[string]struct {
		obj  client.Object
		want []string
	}{
		"object with no owner references returns nil": {
			obj:  utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			want: nil,
		},
		"object with single owner returns its UID": {
			obj:  utiltestingapi.MakeWorkload("wl", "ns").OwnerReference(schema.GroupVersionKind{}, "", "uid-abc").Obj(),
			want: []string{"uid-abc"},
		},
		"object with multiple owners returns all UIDs in order": {
			obj: utiltestingapi.MakeWorkload("wl", "ns").
				OwnerReference(schema.GroupVersionKind{}, "", "uid-1").
				OwnerReference(schema.GroupVersionKind{}, "", "uid-2").
				OwnerReference(schema.GroupVersionKind{}, "", "uid-3").
				Obj(),
			want: []string{"uid-1", "uid-2", "uid-3"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := indexer.IndexOwnerUID(tc.obj)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIndexPodWorkloadSliceName(t *testing.T) {
	cases := map[string]struct {
		obj  client.Object
		want []string
	}{
		"non-Pod returns nil": {
			obj:  utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			want: nil,
		},
		"pod with no annotations returns nil": {
			obj:  testingpod.MakePod("pod", "ns").Obj(),
			want: nil,
		},
		"pod with WorkloadSliceNameAnnotation": {
			obj:  testingpod.MakePod("pod", "ns").Annotation(kueue.WorkloadSliceNameAnnotation, "slice-123").Obj(),
			want: []string{"slice-123"},
		},
		"pod with only WorkloadAnnotation falls back to it": {
			obj:  testingpod.MakePod("pod", "ns").Annotation(kueue.WorkloadAnnotation, "wl-abc").Obj(),
			want: []string{"wl-abc"},
		},
		"pod with both annotations prefers WorkloadSliceNameAnnotation": {
			obj: testingpod.MakePod("pod", "ns").
				Annotation(kueue.WorkloadSliceNameAnnotation, "slice-123").
				Annotation(kueue.WorkloadAnnotation, "wl-abc").
				Obj(),
			want: []string{"slice-123"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := indexer.IndexPodWorkloadSliceName(tc.obj)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIndexWorkloadAdmissionCheck(t *testing.T) {
	cases := map[string]struct {
		obj  client.Object
		want []string
	}{
		"non-Workload returns nil": {
			obj:  utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			want: nil,
		},
		"workload with no admission checks returns nil": {
			obj:  utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			want: nil,
		},
		"workload with single admission check": {
			obj:  utiltestingapi.MakeWorkload("wl", "ns").AdmissionChecks(kueue.AdmissionCheckState{Name: "check-a"}).Obj(),
			want: []string{"check-a"},
		},
		"workload with multiple admission checks": {
			obj: utiltestingapi.MakeWorkload("wl", "ns").
				AdmissionChecks(kueue.AdmissionCheckState{Name: "check-a"}, kueue.AdmissionCheckState{Name: "check-b"}).
				Obj(),
			want: []string{"check-a", "check-b"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := indexer.IndexWorkloadAdmissionCheck(tc.obj)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIndexWorkloadPriorityClass(t *testing.T) {
	cases := map[string]struct {
		obj  client.Object
		want []string
	}{
		"non-Workload returns nil": {
			obj:  utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			want: nil,
		},
		"workload with no priority class ref returns nil": {
			obj:  utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			want: nil,
		},
		"workload with wrong kind is ignored": {
			obj: utiltestingapi.MakeWorkload("wl", "ns").
				PriorityClassRef(&kueue.PriorityClassRef{
					Group: kueue.WorkloadPriorityClassGroup,
					Kind:  kueue.PodPriorityClassKind,
					Name:  "my-pc",
				}).
				Obj(),
			want: nil,
		},
		"workload with wrong group is ignored": {
			obj: utiltestingapi.MakeWorkload("wl", "ns").
				PriorityClassRef(&kueue.PriorityClassRef{
					Group: "other.io",
					Kind:  kueue.WorkloadPriorityClassKind,
					Name:  "my-pc",
				}).
				Obj(),
			want: nil,
		},
		"workload with correct WorkloadPriorityClass ref": {
			obj:  utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high-priority").Obj(),
			want: []string{"high-priority"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := indexer.IndexWorkloadPriorityClass(tc.obj)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIndexDeviceClassExtendedResourceName(t *testing.T) {
	cases := map[string]struct {
		obj  client.Object
		want []string
	}{
		"non-DeviceClass returns nil": {
			obj:  utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			want: nil,
		},
		"DeviceClass with nil ExtendedResourceName returns nil": {
			obj:  utiltesting.MakeDeviceClass("dc").Obj(),
			want: nil,
		},
		"DeviceClass with empty ExtendedResourceName returns nil": {
			obj:  utiltesting.MakeDeviceClass("dc").ExtendedResourceName("").Obj(),
			want: nil,
		},
		"DeviceClass with valid ExtendedResourceName": {
			obj:  utiltesting.MakeDeviceClass("dc").ExtendedResourceName("example.com/gpu").Obj(),
			want: []string{"example.com/gpu"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := indexer.IndexDeviceClassExtendedResourceName(tc.obj)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// fakeFieldIndexer implements client.FieldIndexer for testing Setup().
// It returns noMatchErr for DeviceClass objects when set, simulating a cluster
// where the DeviceClass API is not available.
type fakeFieldIndexer struct {
	noMatchErr error
}

func (f *fakeFieldIndexer) IndexField(_ context.Context, obj client.Object, _ string, _ client.IndexerFunc) error {
	if _, ok := obj.(*resourceapi.DeviceClass); ok && f.noMatchErr != nil {
		return f.noMatchErr
	}
	return nil
}

func TestSetupToleratesNoMatchErrorForDeviceClass(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.KueueDRAIntegrationExtendedResource, true)

	noMatchErr := &apimeta.NoKindMatchError{
		GroupKind:        schema.GroupKind{Group: "resource.k8s.io", Kind: "DeviceClass"},
		SearchedVersions: []string{"v1"},
	}

	cases := map[string]struct {
		indexer *fakeFieldIndexer
		wantErr bool
	}{
		"DeviceClass API available": {
			indexer: &fakeFieldIndexer{},
			wantErr: false,
		},
		"DeviceClass API not available (NoKindMatchError)": {
			indexer: &fakeFieldIndexer{noMatchErr: noMatchErr},
			wantErr: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := indexer.Setup(t.Context(), tc.indexer)
			if (err != nil) != tc.wantErr {
				t.Errorf("Setup() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestIndexWorkloadExtendedResources(t *testing.T) {
	cases := map[string]struct {
		obj  client.Object
		want []string
	}{
		"non-Workload returns nil": {
			obj:  utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			want: nil,
		},
		"workload with only cpu and memory": {
			obj: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).Containers(
					*utiltesting.MakeContainer().Name("c").
						WithResourceReq(corev1.ResourceCPU, "1").
						WithResourceReq(corev1.ResourceMemory, "1Gi").
						Obj(),
				).Obj()).
				Obj(),
			want: nil,
		},
		"workload with single extended resource": {
			obj: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).Containers(
					*utiltesting.MakeContainer().Name("c").WithResourceReq("nvidia.com/gpu", "1").Obj(),
				).Obj()).
				Obj(),
			want: []string{"nvidia.com/gpu"},
		},
		"workload with extended resource only in limits": {
			obj: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).Containers(
					*utiltesting.MakeContainer().Name("c").WithResourceLimit("nvidia.com/gpu", "1").Obj(),
				).Obj()).
				Obj(),
			want: []string{"nvidia.com/gpu"},
		},
		"workload with multiple extended resources": {
			obj: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).Containers(
					*utiltesting.MakeContainer().Name("c1").WithResourceReq("nvidia.com/gpu", "1").Obj(),
					*utiltesting.MakeContainer().Name("c2").WithResourceReq("google.com/tpu", "2").Obj(),
				).Obj()).
				Obj(),
			want: []string{"google.com/tpu", "nvidia.com/gpu"},
		},
		"duplicate extended resource across containers is deduplicated": {
			obj: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).Containers(
					*utiltesting.MakeContainer().Name("c1").WithResourceReq("nvidia.com/gpu", "1").Obj(),
					*utiltesting.MakeContainer().Name("c2").WithResourceReq("nvidia.com/gpu", "2").Obj(),
				).Obj()).
				Obj(),
			want: []string{"nvidia.com/gpu"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := indexer.IndexWorkloadExtendedResources(tc.obj)
			if diff := cmp.Diff(tc.want, got,
				cmpopts.SortSlices(func(a, b string) bool { return a < b }),
				cmpopts.EquateEmpty(),
			); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIndexDynamicQuotaOrchestratorCapacityProvider(t *testing.T) {
	cases := map[string]struct {
		obj  client.Object
		want []string
	}{
		"not DynamicQuotaOrchestrator": {
			obj:  &kueue.Workload{},
			want: nil,
		},
		"no providers": {
			obj:  &kueuealpha.DynamicQuotaOrchestrator{},
			want: []string{},
		},
		"multiple providers": {
			obj: &kueuealpha.DynamicQuotaOrchestrator{
				Spec: kueuealpha.DynamicQuotaOrchestratorSpec{
					CapacityDiscovery: kueuealpha.CapacityDiscovery{
						Providers: []kueuealpha.CapacityDiscoveryProviderContribution{
							{Name: "cp1"},
							{Name: "cp2"},
						},
					},
				},
			},
			want: []string{"cp1", "cp2"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := indexer.IndexDynamicQuotaOrchestratorCapacityProvider(tc.obj)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIndexDynamicQuotaOrchestratorIsDistributing(t *testing.T) {
	cases := map[string]struct {
		obj  client.Object
		want []string
	}{
		"not DynamicQuotaOrchestrator": {
			obj:  &kueue.Workload{},
			want: nil,
		},
		"discovery-only DQO": {
			obj:  &kueuealpha.DynamicQuotaOrchestrator{},
			want: nil,
		},
		"distributing DQO": {
			obj: &kueuealpha.DynamicQuotaOrchestrator{
				Spec: kueuealpha.DynamicQuotaOrchestratorSpec{
					CapacityDistribution: &kueuealpha.CapacityDistribution{
						SubtreeRootQuotaRef: kueuealpha.CapacityDistributionSubtreeRootRef{
							Kind: kueuealpha.CohortSubtreeRootRefKind,
							Name: "root",
						},
					},
				},
			},
			want: []string{"true"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := indexer.IndexDynamicQuotaOrchestratorIsDistributing(tc.obj)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIndexClusterQueueCohort(t *testing.T) {
	cases := map[string]struct {
		obj  client.Object
		want []string
	}{
		"not ClusterQueue": {
			obj:  &kueue.Workload{},
			want: nil,
		},
		"typed nil ClusterQueue": {
			obj:  (*kueue.ClusterQueue)(nil),
			want: nil,
		},
		"empty cohort": {
			obj:  &kueue.ClusterQueue{},
			want: nil,
		},
		"with cohort": {
			obj: &kueue.ClusterQueue{
				Spec: kueue.ClusterQueueSpec{
					CohortName: "my-cohort",
				},
			},
			want: []string{"my-cohort"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := indexer.IndexClusterQueueCohort(tc.obj)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIndexCohortParent(t *testing.T) {
	cases := map[string]struct {
		obj  client.Object
		want []string
	}{
		"not Cohort": {
			obj:  &kueue.Workload{},
			want: nil,
		},
		"typed nil Cohort": {
			obj:  (*kueue.Cohort)(nil),
			want: nil,
		},
		"empty parent": {
			obj:  &kueue.Cohort{},
			want: nil,
		},
		"with parent": {
			obj: &kueue.Cohort{
				Spec: kueue.CohortSpec{
					ParentName: "root-cohort",
				},
			},
			want: []string{"root-cohort"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := indexer.IndexCohortParent(tc.obj)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
