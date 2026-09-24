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

package tas

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func TestBelongsTo(t *testing.T) {
	cases := map[string]struct {
		domainID     TopologyDomainID
		targetDomain TopologyDomainID
		want         bool
	}{
		"empty target domain": {
			domainID: DomainID([]string{"b1", "rack-a", "x1"}),
			want:     true,
		},
		"same domain": {
			domainID:     DomainID([]string{"b1", "rack-a"}),
			targetDomain: DomainID([]string{"b1", "rack-a"}),
			want:         true,
		},
		"ancestor domain": {
			domainID:     DomainID([]string{"b1", "rack-a", "x1"}),
			targetDomain: DomainID([]string{"b1", "rack-a"}),
			want:         true,
		},
		"string-prefix sibling domain": {
			domainID:     DomainID([]string{"b1", "rack-ab", "x1"}),
			targetDomain: DomainID([]string{"b1", "rack-a"}),
			want:         false,
		},
		"different root domain": {
			domainID:     DomainID([]string{"b2", "rack-a", "x1"}),
			targetDomain: DomainID([]string{"b1"}),
			want:         false,
		},
		"target descendant": {
			domainID:     DomainID([]string{"b1", "rack-a"}),
			targetDomain: DomainID([]string{"b1", "rack-a", "x1"}),
			want:         false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.domainID.BelongsTo(tc.targetDomain); got != tc.want {
				t.Errorf("%q.BelongsTo(%q) = %t, want %t", tc.domainID, tc.targetDomain, got, tc.want)
			}
		})
	}
}

func TestNodeNameFromDomainID(t *testing.T) {
	cases := map[string]struct {
		levels       []string
		domainID     TopologyDomainID
		wantNodeName string
		wantOK       bool
	}{
		"hostname is the only level": {
			levels:       []string{corev1.LabelHostname},
			domainID:     DomainID([]string{"x1"}),
			wantNodeName: "x1",
			wantOK:       true,
		},
		// When the hostname is the lowest level, the assignment covers that level
		// alone, so the domain ID holds only the node name even for a deeper
		// topology. See buildAssignment in pkg/cache/scheduler/tas_flavor_snapshot.go.
		"hostname is the lowest of multiple levels": {
			levels:       []string{"cloud.com/topology-block", "cloud.com/topology-rack", corev1.LabelHostname},
			domainID:     DomainID([]string{"x1"}),
			wantNodeName: "x1",
			wantOK:       true,
		},
		"hostname is not the lowest level": {
			levels:   []string{corev1.LabelHostname, "cloud.com/topology-rack"},
			domainID: DomainID([]string{"x1", "r1"}),
			wantOK:   false,
		},
		"lowest level is not hostname": {
			levels:   []string{"cloud.com/topology-block", "cloud.com/topology-rack"},
			domainID: DomainID([]string{"b1", "r1"}),
			wantOK:   false,
		},
		"no levels": {
			levels:   nil,
			domainID: DomainID([]string{"x1"}),
			wantOK:   false,
		},
		// The domain ID is returned verbatim, so an empty one maps to the empty
		// node name, which matches no UnhealthyNodes entry.
		"empty domain ID": {
			levels:       []string{corev1.LabelHostname},
			domainID:     "",
			wantNodeName: "",
			wantOK:       true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotNodeName, gotOK := NodeNameFromDomainID(tc.levels, tc.domainID)
			if gotOK != tc.wantOK {
				t.Errorf("NodeNameFromDomainID() ok = %v, want %v", gotOK, tc.wantOK)
			}
			if gotNodeName != tc.wantNodeName {
				t.Errorf("NodeNameFromDomainID() nodeName = %q, want %q", gotNodeName, tc.wantNodeName)
			}
		})
	}
}

func TestGroupKeyForPodSet(t *testing.T) {
	cases := map[string]struct {
		podSet *kueue.PodSet
		want   PodSetGroupKey
	}{
		"no topology request": {
			podSet: &kueue.PodSet{Name: "workers"},
			want:   "podset/workers",
		},
		"topology request without group name": {
			podSet: &kueue.PodSet{
				Name:            "workers",
				TopologyRequest: &kueue.PodSetTopologyRequest{Required: ptr.To(corev1.LabelHostname)},
			},
			want: "podset/workers",
		},
		"group name set": {
			podSet: &kueue.PodSet{
				Name:            "workers",
				TopologyRequest: &kueue.PodSetTopologyRequest{PodSetGroupName: new("group-a")},
			},
			want: "podsetgroup/group-a",
		},
		// A group name may legitimately equal some other PodSet's name; the
		// two must not collapse into one group. See TestGroupKeyForPodSetNameGroupNameCollision.
		"group name equal to a PodSet name": {
			podSet: &kueue.PodSet{
				Name:            "workers",
				TopologyRequest: &kueue.PodSetTopologyRequest{PodSetGroupName: new("leader")},
			},
			want: "podsetgroup/leader",
		},
		// A group name that itself looks like the PodSet prefix must still not
		// collide, which is why the two prefixes are mutually non-prefixing.
		"group name shaped like the PodSet prefix": {
			podSet: &kueue.PodSet{
				Name:            "workers",
				TopologyRequest: &kueue.PodSetTopologyRequest{PodSetGroupName: new("podset/workers")},
			},
			want: "podsetgroup/podset/workers",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := GroupKeyForPodSet(tc.podSet); got != tc.want {
				t.Errorf("GroupKeyForPodSet() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGroupKeyForPodSetNameGroupNameCollision pins the invariant the prefixes
// exist for: a PodSet named "x" and a group named "x" must stay separate
// groups. Without the prefixes all three PodSets below share one key, forming a
// group of 3 that findLeaderAndWorkers cannot handle - and that
// ValidatePodSetGroupingTopology accepts, because it counts only the PodSets
// that declare the group name.
func TestGroupKeyForPodSetNameGroupNameCollision(t *testing.T) {
	podSetNamedX := &kueue.PodSet{Name: "x"}
	inGroupX := &kueue.PodSet{
		Name:            "b",
		TopologyRequest: &kueue.PodSetTopologyRequest{PodSetGroupName: new("x")},
	}
	alsoInGroupX := &kueue.PodSet{
		Name:            "c",
		TopologyRequest: &kueue.PodSetTopologyRequest{PodSetGroupName: new("x")},
	}

	keyOfX := GroupKeyForPodSet(podSetNamedX)
	keyOfB := GroupKeyForPodSet(inGroupX)
	keyOfC := GroupKeyForPodSet(alsoInGroupX)

	if keyOfX == keyOfB {
		t.Errorf("PodSet %q and group %q must not share a key, both got %q",
			podSetNamedX.Name, *inGroupX.TopologyRequest.PodSetGroupName, keyOfX)
	}
	if keyOfB != keyOfC {
		t.Errorf("PodSets declaring the same group name must share a key, got %q and %q", keyOfB, keyOfC)
	}
}
