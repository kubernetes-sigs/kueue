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

package ranks

import (
	"errors"
	"maps"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestResolveOffset(t *testing.T) {
	basePod := testingpod.MakePod("pod", "ns")
	cases := map[string]struct {
		pods        []*corev1.Pod
		groupOffset int32
		want        int32
		wantErr     error
	}{
		"empty pods returns group offset": {
			groupOffset: 2,
			want:        2,
		},
		"pod without offset annotation returns group offset": {
			pods:        []*corev1.Pod{basePod.Clone().Obj()},
			groupOffset: 2,
			want:        2,
		},
		"pod with offset annotation overrides group offset": {
			pods: []*corev1.Pod{
				basePod.Clone().Annotation(kueue.PodIndexOffsetAnnotation, "0").Obj(),
			},
			groupOffset: 2,
			want:        0,
		},
		"pod with invalid offset annotation returns parse error": {
			pods: []*corev1.Pod{
				basePod.Clone().Annotation(kueue.PodIndexOffsetAnnotation, "not-an-int").Obj(),
			},
			groupOffset: 2,
			wantErr:     ErrParseOffsetAnnotation,
		},
		"pod with out-of-range offset annotation returns parse error": {
			pods: []*corev1.Pod{
				basePod.Clone().Annotation(kueue.PodIndexOffsetAnnotation, "2147483648").Obj(),
			},
			groupOffset: 2,
			wantErr:     ErrParseOffsetAnnotation,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := resolveOffset(tc.pods, tc.groupOffset)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("resolveOffset() error = %v, want %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("resolveOffset() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestReadRanksForLabels(t *testing.T) {
	basePod := testingpod.MakePod("pod", "ns")
	psReq := &kueue.PodSetTopologyRequest{
		PodIndexLabel: new(batchv1.JobCompletionIndexAnnotation),
	}

	cases := map[string]struct {
		psa         kueue.PodSetAssignment
		pods        []*corev1.Pod
		groupOffset int32
		wantRanks   map[int]string
		wantErr     error
	}{
		"applies PodIndexOffsetAnnotation override": {
			psa: kueue.PodSetAssignment{Name: "workers", Count: new(int32(2))},
			pods: []*corev1.Pod{
				basePod.Clone().Name("p0").Annotation(kueue.PodIndexOffsetAnnotation, "0").
					Label(batchv1.JobCompletionIndexAnnotation, "0").Obj(),
			},
			groupOffset: 1,
			wantRanks:   map[int]string{0: "p0"},
		},
		"invalid PodIndexOffsetAnnotation returns ErrParseOffsetAnnotation": {
			psa: kueue.PodSetAssignment{Name: "workers", Count: new(int32(2))},
			pods: []*corev1.Pod{
				basePod.Clone().Name("p0").Annotation(kueue.PodIndexOffsetAnnotation, "bad").
					Label(batchv1.JobCompletionIndexAnnotation, "0").Obj(),
			},
			wantErr: ErrParseOffsetAnnotation,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ReadRanksForLabels(&tc.psa, tc.pods, psReq, tc.groupOffset)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ReadRanksForLabels() error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			gotNames := make(map[int]string, len(got))
			for rank, pod := range got {
				gotNames[rank] = pod.Name
			}
			if !maps.Equal(gotNames, tc.wantRanks) {
				t.Errorf("ReadRanksForLabels() = %v, want %v", gotNames, tc.wantRanks)
			}
		})
	}
}

func TestPodSetRankOffsets(t *testing.T) {
	makePsa := func(name kueue.PodSetReference, count int32) kueue.PodSetAssignment {
		return kueue.PodSetAssignment{Name: name, Count: new(count)}
	}
	groupedRequest := func(group string) *kueue.PodSetTopologyRequest {
		return &kueue.PodSetTopologyRequest{PodSetGroupName: new(group)}
	}
	cases := map[string]struct {
		psas                    []kueue.PodSetAssignment
		psNameToTopologyRequest map[kueue.PodSetReference]*kueue.PodSetTopologyRequest
		want                    map[kueue.PodSetReference]int32
	}{
		"single PodSet without a group": {
			psas: []kueue.PodSetAssignment{makePsa("main", 4)},
			want: map[kueue.PodSetReference]int32{"main": 0},
		},
		"multiple PodSets without groups get independent zero offsets": {
			psas: []kueue.PodSetAssignment{makePsa("driver", 1), makePsa("executor", 4)},
			psNameToTopologyRequest: map[kueue.PodSetReference]*kueue.PodSetTopologyRequest{
				"driver":   {},
				"executor": {},
			},
			want: map[kueue.PodSetReference]int32{"driver": 0, "executor": 0},
		},
		"grouped leader and workers: workers offset by leader count": {
			psas: []kueue.PodSetAssignment{makePsa("leader", 1), makePsa("workers", 4)},
			psNameToTopologyRequest: map[kueue.PodSetReference]*kueue.PodSetTopologyRequest{
				"leader":  groupedRequest("lws-group"),
				"workers": groupedRequest("lws-group"),
			},
			want: map[kueue.PodSetReference]int32{"leader": 0, "workers": 1},
		},
		"grouped PodSets listed larger-first: smaller still gets offset 0": {
			psas: []kueue.PodSetAssignment{makePsa("workers", 4), makePsa("leader", 1)},
			psNameToTopologyRequest: map[kueue.PodSetReference]*kueue.PodSetTopologyRequest{
				"leader":  groupedRequest("lws-group"),
				"workers": groupedRequest("lws-group"),
			},
			want: map[kueue.PodSetReference]int32{"leader": 0, "workers": 1},
		},
		"grouped pair next to an independent PodSet": {
			psas: []kueue.PodSetAssignment{makePsa("leader", 1), makePsa("workers", 4), makePsa("main", 2)},
			psNameToTopologyRequest: map[kueue.PodSetReference]*kueue.PodSetTopologyRequest{
				"leader":  groupedRequest("lws-group"),
				"workers": groupedRequest("lws-group"),
				"main":    {},
			},
			want: map[kueue.PodSetReference]int32{"leader": 0, "workers": 1, "main": 0},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := PodSetRankOffsets(tc.psas, tc.psNameToTopologyRequest); !maps.Equal(got, tc.want) {
				t.Errorf("PodSetRankOffsets() = %v, want %v", got, tc.want)
			}
		})
	}
}
