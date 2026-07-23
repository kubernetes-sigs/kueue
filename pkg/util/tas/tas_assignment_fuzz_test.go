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
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

const (
	fuzzHostnameAlphabet   = "abcdefghijklmnopqrstuvwxyz0123456789"
	maxFuzzLevels          = 16
	maxFuzzDomains         = maxTopologyAssignmentSlices + 1
	maxFuzzHostnameLength  = 63
	maxFuzzHostnameHyphens = 14
	maxFuzzInputBytes      = maxFuzzDomains * (maxFuzzHostnameLength + 2)
)

func topologyAssignmentFromFuzzInput(levelCount uint8, lowestLevelIsHostname bool, input []byte) *TopologyAssignment {
	input = input[:min(len(input), maxFuzzInputBytes)]
	levelCount = max(1, levelCount%(maxFuzzLevels+1))
	levels := make([]string, levelCount)
	for i := range levels {
		levels[i] = fmt.Sprintf("cloud.example.com/level-%d", i)
	}
	if lowestLevelIsHostname {
		levels[len(levels)-1] = corev1.LabelHostname
	}

	var rawDomains [][]byte
	if len(input) > 0 {
		rawDomains = bytes.SplitN(input, []byte{0}, maxFuzzDomains+1)
		if len(rawDomains) > maxFuzzDomains {
			rawDomains = rawDomains[:maxFuzzDomains]
		}
	}

	domains := make([]TopologyDomainAssignment, len(rawDomains))
	for i, rawDomain := range rawDomains {
		countByte := byte(0)
		if len(rawDomain) > 0 {
			countByte = rawDomain[0]
			rawDomain = rawDomain[1:]
		}

		values := make([]string, len(levels))
		for levelIdx := range values {
			values[levelIdx] = fmt.Sprintf("level-%02d-domain-%02x", levelIdx, countByte%8)
		}
		values[len(values)-1] = fuzzHostname(rawDomain)
		domains[i] = TopologyDomainAssignment{
			Values: values,
			Count:  int32(countByte%5 + 1),
		}
	}

	return &TopologyAssignment{Levels: levels, Domains: domains}
}

func fuzzHostname(input []byte) string {
	if len(input) == 0 {
		return "node"
	}
	input = input[:min(len(input), maxFuzzHostnameLength)]
	hostname := make([]byte, len(input))
	hyphenCount := 0
	for i, b := range input {
		switch {
		case b == '-' && hyphenCount < maxFuzzHostnameHyphens:
			hostname[i] = b
			hyphenCount++
		case b >= 'a' && b <= 'z' || b >= '0' && b <= '9':
			hostname[i] = b
		default:
			hostname[i] = fuzzHostnameAlphabet[int(b)%len(fuzzHostnameAlphabet)]
		}
	}
	if hostname[0] == '-' {
		hostname[0] = 'n'
	}
	if hostname[len(hostname)-1] == '-' {
		hostname[len(hostname)-1] = 'n'
	}
	return string(hostname)
}

func fuzzDomainInput(values ...string) []byte {
	domains := make([][]byte, len(values))
	for i, value := range values {
		domains[i] = append([]byte{byte(i%5 + 1)}, value...)
	}
	return bytes.Join(domains, []byte{0})
}

func verifyTopologyAssignmentJSONRoundTrip(t *testing.T, want *TopologyAssignment, encoded *kueue.TopologyAssignment) {
	t.Helper()
	if got := TotalDomainCount(encoded); got != len(want.Domains) {
		t.Fatalf("unexpected domain count: got %d, want %d", got, len(want.Domains))
	}
	if got := len(encoded.Slices); got > maxTopologyAssignmentSlices {
		t.Fatalf("unexpected slice count: got %d, want at most %d", got, maxTopologyAssignmentSlices)
	}
	for i, slice := range encoded.Slices {
		if slice.DomainCount > maxDomainsPerTopologyAssignmentSlice {
			t.Fatalf("slice %d has %d domains, want at most %d", i, slice.DomainCount, maxDomainsPerTopologyAssignmentSlice)
		}
	}

	data, err := json.Marshal(encoded)
	if err != nil {
		t.Fatalf("marshalling compacted topology assignment: %v", err)
	}
	var wire kueue.TopologyAssignment
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("unmarshalling compacted topology assignment: %v", err)
	}
	got := InternalFrom(&wire)
	if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
		t.Fatalf("unexpected round trip (-want,+got):\n%s", diff)
	}
}

func FuzzTopologyAssignmentCompactionRoundTrip(f *testing.F) {
	f.Add(uint8(1), true, fuzzDomainInput())
	f.Add(uint8(1), true, fuzzDomainInput("node-a"))
	f.Add(uint8(2), true, fuzzDomainInput("pool-a-node-0", "pool-b-node-0", "pool-a-node-1", "pool-b-node-1"))
	f.Add(uint8(1), false, fuzzDomainInput("rack-a", "rack-b"))
	f.Add(uint8(1), true, []byte{1, 0xff, '-', 0x80})
	f.Add(uint8(1), true, bytes.Repeat([]byte{0}, maxFuzzDomains))
	f.Add(uint8(1), true, append([]byte{1}, bytes.Repeat([]byte{'-'}, maxFuzzHostnameLength)...))

	f.Fuzz(func(t *testing.T, levelCount uint8, lowestLevelIsHostname bool, input []byte) {
		want := topologyAssignmentFromFuzzInput(levelCount, lowestLevelIsHostname, input)
		encoded := compactTopologyAssignmentEncodingWithHostnamePrefixRuns(want)
		verifyTopologyAssignmentJSONRoundTrip(t, want, encoded)
	})
}

func TestTopologyAssignmentCompactionRoundTripAcrossLimits(t *testing.T) {
	const domainCount = 120_000
	want := &TopologyAssignment{
		Levels:  []string{corev1.LabelHostname},
		Domains: make([]TopologyDomainAssignment, domainCount),
	}
	for i := range want.Domains {
		want.Domains[i] = TopologyDomainAssignment{
			Values: []string{fmt.Sprintf("pool-%d-node-%06d", i%2, i)},
			Count:  1,
		}
	}

	encoded := compactTopologyAssignmentEncodingWithHostnamePrefixRuns(want)
	if got := len(encoded.Slices); got != 2 {
		t.Fatalf("unexpected slice count: got %d, want 2", got)
	}
	verifyTopologyAssignmentJSONRoundTrip(t, want, encoded)
}
