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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// The magnitudes a CPU Quantity is decided against, in the milli CPU is
// accounted in. MaxInt64 cores is the largest one a Quantity carries.
const (
	cpuCeiling      = "9223372036854775807000"
	cpuPastCeiling  = "9223372036854775807001"
	cpuBelowCeiling = "9223372036854775806999"
)

// What the API reports has to be the amount, and what it cannot report has to
// say so and land on the documented ceiling.
func TestAmountQuantity(t *testing.T) {
	f := NewResourceFormatter()

	cases := map[string]struct {
		name      corev1.ResourceName
		amount    Amount
		want      string
		wantExact bool
	}{
		"whole cores in milli":            {name: corev1.ResourceCPU, amount: NewAmount(2000), want: "2", wantExact: true},
		"a fraction of a core":            {name: corev1.ResourceCPU, amount: NewAmount(1500), want: "1500m", wantExact: true},
		"the largest int64 milli":         {name: corev1.ResourceCPU, amount: NewAmount(math.MaxInt64), want: "9223372036854775807m", wantExact: true},
		"one milli past the largest":      {name: corev1.ResourceCPU, amount: bigAmount(t, "9223372036854775808"), want: "9223372036854775808m", wantExact: true},
		"10P of cpu past int64":           {name: corev1.ResourceCPU, amount: cpuAmount(t, "10P"), want: "10P", wantExact: true},
		"a milli past 10P":                {name: corev1.ResourceCPU, amount: bigAmount(t, "10000000000000000001"), want: "10000000000000000001m", wantExact: true},
		"1E of cpu past int64":            {name: corev1.ResourceCPU, amount: cpuAmount(t, "1E"), want: "1E", wantExact: true},
		"sixteen of the largest int64":    {name: corev1.ResourceCPU, amount: bigAmount(t, "147573952589676412912"), want: "147573952589676412912m", wantExact: true},
		"a milli below the cpu ceiling":   {name: corev1.ResourceCPU, amount: bigAmount(t, cpuBelowCeiling), want: "9223372036854775806999m", wantExact: true},
		"the cpu ceiling":                 {name: corev1.ResourceCPU, amount: bigAmount(t, cpuCeiling), want: "9223372036854775807", wantExact: true},
		"a milli past the cpu ceiling":    {name: corev1.ResourceCPU, amount: bigAmount(t, cpuPastCeiling), want: "9223372036854775807", wantExact: false},
		"far past the cpu ceiling":        {name: corev1.ResourceCPU, amount: bigAmount(t, "9223372036854775807000000"), want: "9223372036854775807", wantExact: false},
		"the negative cpu ceiling":        {name: corev1.ResourceCPU, amount: bigAmount(t, "-"+cpuCeiling), want: "-9223372036854775807", wantExact: true},
		"a milli past it negative":        {name: corev1.ResourceCPU, amount: bigAmount(t, "-"+cpuPastCeiling), want: "-9223372036854775807", wantExact: false},
		"sixteen of the largest negative": {name: corev1.ResourceCPU, amount: bigAmount(t, "-147573952589676412912"), want: "-147573952589676412912m", wantExact: true},

		"whole devices":               {name: "example.com/gpu", amount: NewAmount(8), want: "8", wantExact: true},
		"the largest int64 device":    {name: "example.com/gpu", amount: NewAmount(math.MaxInt64), want: "9223372036854775807", wantExact: true},
		"one past the largest device": {name: "example.com/gpu", amount: bigAmount(t, "9223372036854775808"), want: "9223372036854775807", wantExact: false},
		"the largest negative device": {name: "example.com/gpu", amount: NewAmount(-math.MaxInt64), want: "-9223372036854775807", wantExact: true},
		// MinInt64 fits an int64 and is one past the magnitude a Quantity carries.
		"the smallest int64 device": {name: "example.com/gpu", amount: NewAmount(math.MinInt64), want: "-9223372036854775807", wantExact: false},
		"far past in the negative":  {name: "example.com/gpu", amount: bigAmount(t, "-18446744073709551614"), want: "-9223372036854775807", wantExact: false},
		"memory past int64":         {name: corev1.ResourceMemory, amount: bigAmount(t, "9223372036854775808"), want: "9223372036854775807", wantExact: false},
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
			// The Quantity has to be the number, not only a string that reads
			// back as itself: an output of zero round trips too.
			want := tc.amount
			if !tc.wantExact {
				want = quantityCeiling(t, tc.name, tc.amount.Sign())
			}
			if back := AmountFromQuantity(tc.name, q); !back.Equal(want) {
				t.Errorf("came back as %s, want %s", back, want)
			}
			// What is written has to read back as the same number.
			b, err := json.Marshal(q)
			if err != nil {
				t.Fatalf("Marshal() = %v", err)
			}
			var read resource.Quantity
			if err := json.Unmarshal(b, &read); err != nil {
				t.Fatalf("Unmarshal(%s) = %v", b, err)
			}
			if read.Cmp(q) != 0 {
				t.Errorf("round trip of %s came back as %s", q.String(), read.String())
			}
		})
	}
}

// quantityCeiling returns the amount a capped value has to land on: the largest
// magnitude a Quantity carries, in the unit the resource is accounted in.
func quantityCeiling(t *testing.T, name corev1.ResourceName, sign int) Amount {
	t.Helper()
	digits := "9223372036854775807"
	if name == corev1.ResourceCPU {
		digits = cpuCeiling
	}
	if sign < 0 {
		digits = "-" + digits
	}
	return bigAmount(t, digits)
}

// A CPU amount stays ordered as it grows. Reporting a milli past a whole number
// of cores by capping would make an increase read as a decrease.
func TestAmountQuantityIsMonotonic(t *testing.T) {
	f := NewResourceFormatter()

	steps := []Amount{
		NewAmount(math.MaxInt64),
		bigAmount(t, "9223372036854775808"),
		cpuAmount(t, "10P"),
		bigAmount(t, "10000000000000000001"),
		cpuAmount(t, "1E"),
		bigAmount(t, cpuBelowCeiling),
		bigAmount(t, cpuCeiling),
		bigAmount(t, cpuPastCeiling),
	}
	for i := 1; i < len(steps); i++ {
		if steps[i].Cmp(steps[i-1]) <= 0 {
			t.Fatalf("the fixture is not increasing at %d: %s then %s", i, steps[i-1], steps[i])
		}
		prev, _ := f.AmountQuantity(corev1.ResourceCPU, steps[i-1])
		next, _ := f.AmountQuantity(corev1.ResourceCPU, steps[i])
		if next.Cmp(prev) < 0 {
			t.Errorf("%s reports %s, less than %s reports for %s",
				steps[i], next.String(), prev.String(), steps[i-1])
		}
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

// The JSON shape is an int64 projection kept for diagnostics, not a round trip.
// Two amounts that differ only past int64 come out as the same number, which is
// worth pinning so nobody reads the output as recoverable.
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

func cpuAmount(t *testing.T, s string) Amount {
	t.Helper()
	return AmountFromQuantity(corev1.ResourceCPU, resource.MustParse(s))
}
