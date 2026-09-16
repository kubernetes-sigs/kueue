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

package resources

import (
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// A CPU aggregate past int64 in milli reaches the API as the number it is.
func TestToResourceListScalesBeforeNarrowing(t *testing.T) {
	half := cpuAmount("5P")
	frq := FlavorResourceQuantities{
		{Flavor: "a", Resource: corev1.ResourceCPU}: half,
		{Flavor: "b", Resource: corev1.ResourceCPU}: half,
	}
	got := frq.ToResourceList()
	if q := got[corev1.ResourceCPU]; q.String() != "10P" {
		t.Errorf("cpu = %s, want 10P", q.String())
	}
	if got := (FlavorResourceQuantities{}).ToResourceList(); got != nil {
		t.Errorf("ToResourceList() of nothing = %v, want nil", got)
	}
}

// The JSON shape is a lossy int64 projection kept for diagnostics.
func TestFlavorResourceQuantitiesMarshalJSONIsALossyProjection(t *testing.T) {
	fr := FlavorResource{Flavor: "a", Resource: "example.com/gpu"}
	one := FlavorResourceQuantities{fr: bigAmount(t, "9223372036854775808")}
	far := FlavorResourceQuantities{fr: bigAmount(t, "92233720368547758080000")}

	a, err := json.Marshal(one)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	b, err := json.Marshal(far)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	if string(a) != string(b) {
		t.Errorf("two amounts past int64 marshalled differently: %s and %s", a, b)
	}
	if !strings.Contains(string(a), "9223372036854775807") {
		t.Errorf("expected the int64 ceiling in %s", a)
	}

	neg := FlavorResourceQuantities{fr: bigAmount(t, "-92233720368547758080000")}
	c, err := json.Marshal(neg)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	if !strings.Contains(string(c), "-9223372036854775808") {
		t.Errorf("expected the int64 floor in %s", c)
	}
}
