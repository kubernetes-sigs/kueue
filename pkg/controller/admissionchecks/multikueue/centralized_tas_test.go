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

package multikueue

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

const clusterLabel = "kueue.x-k8s.io/multikueue-cluster"

// managerAssignment builds a compressed API TopologyAssignment from the given
// internal (cluster, host, count) domains, matching what the manager scheduler
// produces with the spike's [cluster-label, hostname] topology.
func managerAssignment(domains []utiltas.TopologyDomainAssignment) *kueue.TopologyAssignment {
	return utiltas.V1Beta2From(&utiltas.TopologyAssignment{
		Levels:  []string{clusterLabel, corev1.LabelHostname},
		Domains: domains,
	})
}

func TestProjectAdmissionForWorker(t *testing.T) {
	cases := map[string]struct {
		admission      *kueue.Admission
		wantOK         bool
		wantCluster    string
		wantHostCounts map[string]int32
	}{
		"no admission yet": {
			admission: nil,
			wantOK:    false,
		},
		"delayed (no topology assignment yet)": {
			admission: &kueue.Admission{
				PodSetAssignments: []kueue.PodSetAssignment{{Name: "main"}},
			},
			wantOK: false,
		},
		"single cluster, host-exact projection": {
			admission: &kueue.Admission{
				ClusterQueue: "central-cq",
				PodSetAssignments: []kueue.PodSetAssignment{{
					Name: "main",
					TopologyAssignment: managerAssignment([]utiltas.TopologyDomainAssignment{
						{Values: []string{"worker1", "node-a"}, Count: 2},
						{Values: []string{"worker1", "node-b"}, Count: 1},
					}),
				}},
			},
			wantOK:         true,
			wantCluster:    "worker1",
			wantHostCounts: map[string]int32{"node-a": 2, "node-b": 1},
		},
		"assignment spanning two clusters is rejected": {
			admission: &kueue.Admission{
				PodSetAssignments: []kueue.PodSetAssignment{{
					Name: "main",
					TopologyAssignment: managerAssignment([]utiltas.TopologyDomainAssignment{
						{Values: []string{"worker1", "node-a"}, Count: 1},
						{Values: []string{"worker2", "node-c"}, Count: 1},
					}),
				}},
			},
			wantOK: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			wl := &kueue.Workload{}
			wl.Status.Admission = tc.admission

			gotCluster, gotAdmission, gotOK := projectAdmissionForWorker(wl)
			if gotOK != tc.wantOK {
				t.Fatalf("ok = %v, want %v", gotOK, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if gotCluster != tc.wantCluster {
				t.Errorf("cluster = %q, want %q", gotCluster, tc.wantCluster)
			}

			psa := gotAdmission.PodSetAssignments[0]
			internal := utiltas.InternalFrom(psa.TopologyAssignment)
			if diff := cmp.Diff([]string{corev1.LabelHostname}, internal.Levels); diff != "" {
				t.Errorf("worker levels mismatch (-want +got):\n%s", diff)
			}
			gotCounts := make(map[string]int32, len(internal.Domains))
			for _, d := range internal.Domains {
				if len(d.Values) != 1 {
					t.Fatalf("worker domain should have exactly one (hostname) value, got %v", d.Values)
				}
				gotCounts[d.Values[0]] = d.Count
			}
			if diff := cmp.Diff(tc.wantHostCounts, gotCounts); diff != "" {
				t.Errorf("host counts mismatch (-want +got):\n%s", diff)
			}
			if psa.DelayedTopologyRequest != nil {
				t.Errorf("DelayedTopologyRequest should be cleared, got %v", *psa.DelayedTopologyRequest)
			}
		})
	}
}
