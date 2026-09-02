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
	"math"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// What the API reports has to be the amount, and what it cannot report has to
// say so rather than serializing to something else.
func TestAmountQuantity(t *testing.T) {
	f := NewResourceFormatter()

	cases := map[string]struct {
		name      corev1.ResourceName
		amount    Amount
		want      string
		wantExact bool
	}{
		"whole cores in milli":     {name: corev1.ResourceCPU, amount: NewAmount(2000), want: "2", wantExact: true},
		"a fraction of a core":     {name: corev1.ResourceCPU, amount: NewAmount(1500), want: "1500m", wantExact: true},
		"the largest int64 milli":  {name: corev1.ResourceCPU, amount: NewAmount(math.MaxInt64), want: "9223372036854775807m", wantExact: true},
		"10P of cpu past int64":    {name: corev1.ResourceCPU, amount: cpuAmount(t, "10P"), want: "10P", wantExact: true},
		"1E of cpu past int64":     {name: corev1.ResourceCPU, amount: cpuAmount(t, "1E"), want: "1E", wantExact: true},
		"whole devices":            {name: "example.com/gpu", amount: NewAmount(8), want: "8", wantExact: true},
		"the largest int64 device": {name: "example.com/gpu", amount: NewAmount(math.MaxInt64), want: "9223372036854775807", wantExact: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			q, exact := f.AmountQuantity(tc.name, tc.amount)
			if exact != tc.wantExact {
				t.Errorf("exact = %v, want %v", exact, tc.wantExact)
			}
			if got := q.String(); got != tc.want {
				t.Errorf("String() = %s, want %s", got, tc.want)
			}
			// What is written has to read back as the same number.
			b, err := json.Marshal(q)
			if err != nil {
				t.Fatalf("Marshal() = %v", err)
			}
			var back resource.Quantity
			if err := json.Unmarshal(b, &back); err != nil {
				t.Fatalf("Unmarshal(%s) = %v", b, err)
			}
			if back.Cmp(q) != 0 {
				t.Errorf("round trip of %s came back as %s", q.String(), back.String())
			}
		})
	}
}

// A value the Quantity range cannot carry is capped and says so, rather than
// being handed to a serializer that can drop its exponent.
func TestAmountQuantityCapsWhatItCannotReport(t *testing.T) {
	f := NewResourceFormatter()
	past := NewAmount(math.MaxInt64)
	for range 4 {
		past = past.Add(past)
	}

	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, "example.com/gpu", corev1.ResourceMemory} {
		t.Run(string(name), func(t *testing.T) {
			q, exact := f.AmountQuantity(name, past)
			if exact {
				t.Error("exact = true for a value past the reported range")
			}
			back, err := resource.ParseQuantity(q.String())
			if err != nil {
				t.Fatalf("ParseQuantity(%s) = %v", q.String(), err)
			}
			if back.Cmp(q) != 0 {
				t.Errorf("the capped value does not read back: %s -> %s", q.String(), back.String())
			}
		})
	}
}

// The formatter owns the format, so a registered byte-valued resource keeps
// BinarySI on the path this change adds.
func TestAmountQuantityKeepsTheRegisteredFormat(t *testing.T) {
	f := NewResourceFormatter()
	f.RegisterBinaryFormattedResource("example.com/memory")

	q, exact := f.AmountQuantity("example.com/memory", NewAmount(2*1024*1024*1024))
	if !exact {
		t.Error("exact = false for a value inside the range")
	}
	if got := q.String(); got != "2Gi" {
		t.Errorf("String() = %s, want 2Gi", got)
	}
}

// A CPU aggregate past int64 in milli reaches the API as the number it is.
func TestToResourceListScalesBeforeNarrowing(t *testing.T) {
	half := cpuAmount(t, "5P")
	frq := FlavorResourceQuantities{
		{Flavor: "a", Resource: corev1.ResourceCPU}: half,
		{Flavor: "b", Resource: corev1.ResourceCPU}: half,
	}
	got := frq.ToResourceList(NewResourceFormatter())
	if q := got[corev1.ResourceCPU]; q.String() != "10P" {
		t.Errorf("cpu = %s, want 10P", q.String())
	}
}

func cpuAmount(t *testing.T, s string) Amount {
	t.Helper()
	return AmountFromQuantity(corev1.ResourceCPU, resource.MustParse(s))
}
