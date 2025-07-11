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

package workload

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

func testPodSet(name string, count int32) kueue.PodSet {
	return kueue.PodSet{
		Name:  kueue.PodSetReference(name),
		Count: count,
	}
}

func TestExtractPodSetCounts(t *testing.T) {
	type args struct {
		podSets []kueue.PodSet
	}

	tests := map[string]struct {
		args args
		want PodSetsCounts
	}{
		"EmptyPodSets": {},
		"SinglePodSet": {
			args: args{
				podSets: []kueue.PodSet{testPodSet("test", 10)},
			},
			want: PodSetsCounts{
				"test": 10,
			},
		},
		"MultiplePodSets": {
			args: args{
				podSets: []kueue.PodSet{testPodSet("test-a", 10), testPodSet("test-b", 11)},
			},
			want: PodSetsCounts{
				"test-a": 10,
				"test-b": 11,
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(ExtractPodSetCounts(tt.args.podSets), tt.want); diff != "" {
				t.Errorf("ExtractPodSetCounts() got(-),want(+): %s", diff)
			}
		})
	}
}

func TestExtractPodSetCountsFromWorkload(t *testing.T) {
	type args struct {
		workload *kueue.Workload
	}
	tests := map[string]struct {
		args args
		want PodSetsCounts
	}{
		"EmptyPodSets": {
			args: args{
				workload: &kueue.Workload{},
			},
		},
		"SinglePodSet": {
			args: args{
				workload: &kueue.Workload{
					Spec: kueue.WorkloadSpec{PodSets: []kueue.PodSet{testPodSet("test", 10)}},
				},
			},
			want: PodSetsCounts{
				"test": 10,
			},
		},
		"MultiplePodSets": {
			args: args{
				workload: &kueue.Workload{
					Spec: kueue.WorkloadSpec{PodSets: []kueue.PodSet{
						testPodSet("test-a", 10),
						testPodSet("test-b", 11),
					}},
				},
			},
			want: PodSetsCounts{
				"test-a": 10,
				"test-b": 11,
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(ExtractPodSetCountsFromWorkload(tt.args.workload), tt.want); diff != "" {
				t.Errorf("ExtractPodSetCountsFromWorkload() got(-),want(+): %s", diff)
			}
		})
	}
}

func TestApplyPodSetCounts(t *testing.T) {
	type args struct {
		wl     *kueue.Workload
		counts PodSetsCounts
	}
	tests := map[string]struct {
		args args
		want *kueue.Workload
	}{
		"EdgeCase_EmptyWorkloadPodsSets": {
			args: args{
				wl: &kueue.Workload{},
				counts: PodSetsCounts{
					"test": 10,
				},
			},
			want: &kueue.Workload{},
		},
		"EdgeCase_EmptyPodsSetsCounts": {
			args: args{
				wl: &kueue.Workload{
					Spec: kueue.WorkloadSpec{
						PodSets: []kueue.PodSet{
							{
								Name:  "test-a",
								Count: 10,
							},
						},
					},
				},
			},
			want: &kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						{
							Name:  "test-a",
							Count: 10,
						},
					},
				},
			},
		},
		"ApplyCounts": {
			args: args{
				wl: &kueue.Workload{
					Spec: kueue.WorkloadSpec{
						PodSets: []kueue.PodSet{
							{Name: "test-a", Count: 10},
							{Name: "test-b", Count: 11},
						},
					},
				},
				counts: PodSetsCounts{
					"test-a": 20,
					"test-b": 2,
				},
			},
			want: &kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						{Name: "test-a", Count: 20},
						{Name: "test-b", Count: 2},
					},
				},
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			ApplyPodSetCounts(tt.args.wl, tt.args.counts)
			if diff := cmp.Diff(tt.args.wl, tt.want); diff != "" {
				t.Errorf("ApplyPodSetCounts() got(-),want(+): %s", diff)
			}
		})
	}
}

func TestPodSetsCounts_HasFewerReplicasThan(t *testing.T) {
	type args struct {
		in PodSetsCounts
	}
	tests := map[string]struct {
		c    PodSetsCounts
		args args
		want bool
	}{
		"HasFewer": {
			c: PodSetsCounts{
				"test-a": 11,
				"test-b": 2,
			},
			args: args{
				in: PodSetsCounts{
					"test-a": 10,
					"test-b": 20,
				},
			},
			want: true,
		},
		"DoesNotHaveFewer": {
			c: PodSetsCounts{
				"test-a": 11,
				"test-b": 2,
			},
			args: args{
				in: PodSetsCounts{
					"test-a": 10,
					"test-b": 2,
				},
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := tt.c.HasFewerReplicasThan(tt.args.in); got != tt.want {
				t.Errorf("HasFewerReplicasThan() = %v, want %v", got, tt.want)
			}
		})
	}
}
