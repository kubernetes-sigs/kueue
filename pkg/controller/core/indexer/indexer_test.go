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

package indexer

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// helpers to build test objects without importing any package that transitively
// imports this (indexer) package, which would create an import cycle.

func makeWorkload(name, ns string) *kueue.Workload {
	return &kueue.Workload{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
}

func makeLocalQueue(name, ns, cq string) *kueue.LocalQueue {
	return &kueue.LocalQueue{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       kueue.LocalQueueSpec{ClusterQueue: kueue.ClusterQueueReference(cq)},
	}
}

func makeLimitRange(name, ns string, types ...corev1.LimitType) *corev1.LimitRange {
	items := make([]corev1.LimitRangeItem, len(types))
	for i, t := range types {
		items[i] = corev1.LimitRangeItem{Type: t}
	}
	return &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       corev1.LimitRangeSpec{Limits: items},
	}
}

func makePod(name, ns string, annotations map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Annotations: annotations},
	}
}

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
			got := OwnerReferenceIndexKey(tc.gvk)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOwnerReferenceIndexFieldMatcher(t *testing.T) {
	got := OwnerReferenceIndexFieldMatcher(batchJobGVK, "my-job")

	want := client.MatchingFields{".metadata.ownerReferences[batch.Job]": "my-job"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("MatchingFields mismatch (-want +got):\n%s", diff)
	}
}

func TestWorkloadOwnerIndexFunc(t *testing.T) {
	indexFn := WorkloadOwnerIndexFunc(batchJobGVK)

	cases := map[string]struct {
		obj  client.Object
		want []string
	}{
		"non-workload object returns nil": {
			obj:  makeLimitRange("lr", "ns"),
			want: nil,
		},
		"workload with no owner references returns nil": {
			obj:  makeWorkload("wl", "ns"),
			want: nil,
		},
		"workload with non-matching kind is skipped": {
			obj: func() client.Object {
				wl := makeWorkload("wl", "ns")
				wl.OwnerReferences = []metav1.OwnerReference{
					{APIVersion: "batch/v1", Kind: "CronJob", Name: "cron"},
				}
				return wl
			}(),
			want: nil,
		},
		"workload with non-matching apiVersion is skipped": {
			obj: func() client.Object {
				wl := makeWorkload("wl", "ns")
				wl.OwnerReferences = []metav1.OwnerReference{
					{APIVersion: "apps/v1", Kind: "Job", Name: "job"},
				}
				return wl
			}(),
			want: nil,
		},
		"workload with single matching owner": {
			obj: func() client.Object {
				wl := makeWorkload("wl", "ns")
				wl.OwnerReferences = []metav1.OwnerReference{
					{APIVersion: "batch/v1", Kind: "Job", Name: "my-job"},
				}
				return wl
			}(),
			want: []string{"my-job"},
		},
		"workload with multiple owners, only matching ones returned": {
			obj: func() client.Object {
				wl := makeWorkload("wl", "ns")
				wl.OwnerReferences = []metav1.OwnerReference{
					{APIVersion: "batch/v1", Kind: "Job", Name: "job-1"},
					{APIVersion: "apps/v1", Kind: "Deployment", Name: "deploy"},
					{APIVersion: "batch/v1", Kind: "Job", Name: "job-2"},
				}
				return wl
			}(),
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
			obj:  makeWorkload("wl", "ns"),
			want: nil,
		},
		"LocalQueue returns its clusterQueue name": {
			obj:  makeLocalQueue("lq", "ns", "my-cq"),
			want: []string{"my-cq"},
		},
		"LocalQueue with empty clusterQueue": {
			obj:  makeLocalQueue("lq", "ns", ""),
			want: []string{""},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := IndexQueueClusterQueue(tc.obj)
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
			obj:  makeLocalQueue("lq", "ns", "cq"),
			want: nil,
		},
		"workload returns its queue name": {
			obj: func() client.Object {
				wl := makeWorkload("wl", "ns")
				wl.Spec.QueueName = "user-queue"
				return wl
			}(),
			want: []string{"user-queue"},
		},
		"workload with empty queue name": {
			obj:  makeWorkload("wl", "ns"),
			want: []string{""},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := IndexWorkloadQueue(tc.obj)
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
			obj:  makeLocalQueue("lq", "ns", "cq"),
			want: nil,
		},
		"workload without admission returns nil": {
			obj:  makeWorkload("wl", "ns"),
			want: nil,
		},
		"workload with admission returns cluster queue": {
			obj: func() client.Object {
				wl := makeWorkload("wl", "ns")
				wl.Status.Admission = &kueue.Admission{
					ClusterQueue: "my-cq",
				}
				return wl
			}(),
			want: []string{"my-cq"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := IndexWorkloadClusterQueue(tc.obj)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIndexLimitRangeHasContainerType(t *testing.T) {
	cases := map[string]struct {
		obj  client.Object
		want []string
	}{
		"non-LimitRange returns nil": {
			obj:  makeWorkload("wl", "ns"),
			want: nil,
		},
		"LimitRange with no limits returns nil": {
			obj:  makeLimitRange("lr", "ns"),
			want: nil,
		},
		"LimitRange with only Pod type returns nil": {
			obj:  makeLimitRange("lr", "ns", corev1.LimitTypePod),
			want: nil,
		},
		"LimitRange with Container type returns true": {
			obj:  makeLimitRange("lr", "ns", corev1.LimitTypeContainer),
			want: []string{"true"},
		},
		"LimitRange with both Pod and Container types returns true": {
			obj:  makeLimitRange("lr", "ns", corev1.LimitTypePod, corev1.LimitTypeContainer),
			want: []string{"true"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := IndexLimitRangeHasContainerType(tc.obj)
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
			obj:  makeLocalQueue("lq", "ns", "cq"),
			want: nil,
		},
		"workload without QuotaReserved condition returns False": {
			obj:  makeWorkload("wl", "ns"),
			want: []string{"False"},
		},
		"workload with QuotaReserved=True": {
			obj: func() client.Object {
				wl := makeWorkload("wl", "ns")
				wl.Status.Conditions = []metav1.Condition{
					{Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionTrue},
				}
				return wl
			}(),
			want: []string{"True"},
		},
		"workload with QuotaReserved=False": {
			obj: func() client.Object {
				wl := makeWorkload("wl", "ns")
				wl.Status.Conditions = []metav1.Condition{
					{Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionFalse},
				}
				return wl
			}(),
			want: []string{"False"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := IndexWorkloadQuotaReserved(tc.obj)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIndexWorkloadRuntimeClass(t *testing.T) {
	rc1, rc2 := "rc-fast", "rc-slow"

	podSet := func(name string, rc *string) kueue.PodSet {
		return kueue.PodSet{
			Name:     kueue.PodSetReference(name),
			Count:    1,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{RuntimeClassName: rc}},
		}
	}

	cases := map[string]struct {
		obj  client.Object
		want []string
	}{
		"non-Workload returns nil": {
			obj:  makeLocalQueue("lq", "ns", "cq"),
			want: nil,
		},
		"workload with no podsets returns nil": {
			obj:  makeWorkload("wl", "ns"),
			want: nil,
		},
		"podset with no runtime class returns nil": {
			obj: func() client.Object {
				wl := makeWorkload("wl", "ns")
				wl.Spec.PodSets = []kueue.PodSet{podSet("main", nil)}
				return wl
			}(),
			want: nil,
		},
		"workload with single runtime class": {
			obj: func() client.Object {
				wl := makeWorkload("wl", "ns")
				wl.Spec.PodSets = []kueue.PodSet{podSet("main", &rc1)}
				return wl
			}(),
			want: []string{"rc-fast"},
		},
		"workload with multiple distinct runtime classes": {
			obj: func() client.Object {
				wl := makeWorkload("wl", "ns")
				wl.Spec.PodSets = []kueue.PodSet{podSet("ps1", &rc1), podSet("ps2", &rc2)}
				return wl
			}(),
			want: []string{"rc-fast", "rc-slow"},
		},
		"duplicate runtime class across podsets is deduplicated": {
			obj: func() client.Object {
				wl := makeWorkload("wl", "ns")
				wl.Spec.PodSets = []kueue.PodSet{podSet("ps1", &rc1), podSet("ps2", &rc1)}
				return wl
			}(),
			want: []string{"rc-fast"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := IndexWorkloadRuntimeClass(tc.obj)
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
			obj:  makeWorkload("wl", "ns"),
			want: nil,
		},
		"object with single owner returns its UID": {
			obj: func() client.Object {
				wl := makeWorkload("wl", "ns")
				wl.OwnerReferences = []metav1.OwnerReference{{UID: "uid-abc"}}
				return wl
			}(),
			want: []string{"uid-abc"},
		},
		"object with multiple owners returns all UIDs in order": {
			obj: func() client.Object {
				wl := makeWorkload("wl", "ns")
				wl.OwnerReferences = []metav1.OwnerReference{
					{UID: "uid-1"},
					{UID: "uid-2"},
					{UID: "uid-3"},
				}
				return wl
			}(),
			want: []string{"uid-1", "uid-2", "uid-3"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := IndexOwnerUID(tc.obj)
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
			obj:  makeWorkload("wl", "ns"),
			want: nil,
		},
		"pod with no annotations returns nil": {
			obj:  makePod("pod", "ns", nil),
			want: nil,
		},
		"pod with WorkloadSliceNameAnnotation": {
			obj:  makePod("pod", "ns", map[string]string{kueue.WorkloadSliceNameAnnotation: "slice-123"}),
			want: []string{"slice-123"},
		},
		"pod with only WorkloadAnnotation falls back to it": {
			obj:  makePod("pod", "ns", map[string]string{kueue.WorkloadAnnotation: "wl-abc"}),
			want: []string{"wl-abc"},
		},
		"pod with both annotations prefers WorkloadSliceNameAnnotation": {
			obj: makePod("pod", "ns", map[string]string{
				kueue.WorkloadSliceNameAnnotation: "slice-123",
				kueue.WorkloadAnnotation:          "wl-abc",
			}),
			want: []string{"slice-123"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := IndexPodWorkloadSliceName(tc.obj)
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
			obj:  makeLocalQueue("lq", "ns", "cq"),
			want: nil,
		},
		"workload with no admission checks returns nil": {
			obj:  makeWorkload("wl", "ns"),
			want: nil,
		},
		"workload with single admission check": {
			obj: func() client.Object {
				wl := makeWorkload("wl", "ns")
				wl.Status.AdmissionChecks = []kueue.AdmissionCheckState{
					{Name: "check-a"},
				}
				return wl
			}(),
			want: []string{"check-a"},
		},
		"workload with multiple admission checks": {
			obj: func() client.Object {
				wl := makeWorkload("wl", "ns")
				wl.Status.AdmissionChecks = []kueue.AdmissionCheckState{
					{Name: "check-a"},
					{Name: "check-b"},
				}
				return wl
			}(),
			want: []string{"check-a", "check-b"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := IndexWorkloadAdmissionCheck(tc.obj)
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
			obj:  makeLocalQueue("lq", "ns", "cq"),
			want: nil,
		},
		"workload with no priority class ref returns nil": {
			obj:  makeWorkload("wl", "ns"),
			want: nil,
		},
		"workload with wrong kind is ignored": {
			obj: func() client.Object {
				wl := makeWorkload("wl", "ns")
				wl.Spec.PriorityClassRef = &kueue.PriorityClassRef{
					Group: kueue.WorkloadPriorityClassGroup,
					Kind:  kueue.PodPriorityClassKind,
					Name:  "my-pc",
				}
				return wl
			}(),
			want: nil,
		},
		"workload with wrong group is ignored": {
			obj: func() client.Object {
				wl := makeWorkload("wl", "ns")
				wl.Spec.PriorityClassRef = &kueue.PriorityClassRef{
					Group: "other.io",
					Kind:  kueue.WorkloadPriorityClassKind,
					Name:  "my-pc",
				}
				return wl
			}(),
			want: nil,
		},
		"workload with correct WorkloadPriorityClass ref": {
			obj: func() client.Object {
				wl := makeWorkload("wl", "ns")
				wl.Spec.PriorityClassRef = &kueue.PriorityClassRef{
					Group: kueue.WorkloadPriorityClassGroup,
					Kind:  kueue.WorkloadPriorityClassKind,
					Name:  "high-priority",
				}
				return wl
			}(),
			want: []string{"high-priority"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := IndexWorkloadPriorityClass(tc.obj)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIndexDeviceClassExtendedResourceName(t *testing.T) {
	extName := "example.com/gpu"
	empty := ""

	cases := map[string]struct {
		obj  client.Object
		want []string
	}{
		"non-DeviceClass returns nil": {
			obj:  makeWorkload("wl", "ns"),
			want: nil,
		},
		"DeviceClass with nil ExtendedResourceName returns nil": {
			obj:  &resourceapi.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: "dc"}},
			want: nil,
		},
		"DeviceClass with empty ExtendedResourceName returns nil": {
			obj: &resourceapi.DeviceClass{
				ObjectMeta: metav1.ObjectMeta{Name: "dc"},
				Spec:       resourceapi.DeviceClassSpec{ExtendedResourceName: &empty},
			},
			want: nil,
		},
		"DeviceClass with valid ExtendedResourceName": {
			obj: &resourceapi.DeviceClass{
				ObjectMeta: metav1.ObjectMeta{Name: "dc"},
				Spec:       resourceapi.DeviceClassSpec{ExtendedResourceName: &extName},
			},
			want: []string{"example.com/gpu"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := IndexDeviceClassExtendedResourceName(tc.obj)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
