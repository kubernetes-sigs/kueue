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
)

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
