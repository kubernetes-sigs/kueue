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
	"math"
	"testing"
)

// The operands are divided exactly. Converting them to float64 first loses the
// difference above 2^53, where the ratio reads as exactly one thousand and the
// ceiling of it is one short.
func TestPerThousandOfIsExactAboveFloat64Integers(t *testing.T) {
	const twoTo53 = int64(1) << 53
	got := NewAmount(twoTo53 + 1).PerThousandOf(NewAmount(twoTo53))
	if got <= 1000 {
		t.Errorf("(2^53+1)/2^53 = %v, want more than 1000", got)
	}
	if lossy := float64(twoTo53+1) * 1000 / float64(twoTo53); lossy != 1000 {
		t.Fatalf("the float64 path no longer loses this, so the test proves nothing: %v", lossy)
	}
}

// A borrower is a borrower however small its share is. Zero is what a caller
// reads as not borrowing at all.
func TestPerThousandOfDoesNotUnderflowToZero(t *testing.T) {
	lendable := NewAmount(1)
	for range 1400 {
		lendable = lendable.Add(lendable)
	}
	got := NewAmount(1).PerThousandOf(lendable)
	if got <= 0 {
		t.Errorf("a positive ratio came back as %v", got)
	}
}

func TestPerThousandOfZeroLendable(t *testing.T) {
	if got := NewAmount(5).PerThousandOf(Amount{}); got != 0 {
		t.Errorf("PerThousandOf(0) = %v, want 0", got)
	}
}

// A ratio past float64 has to stay ordered above one that is not, so the caller
// that turns it into a share does not read it as smaller.
func TestPerThousandOfHugeRatio(t *testing.T) {
	borrowed := NewAmount(1)
	for range 1400 {
		borrowed = borrowed.Add(borrowed)
	}
	got := borrowed.PerThousandOf(NewAmount(1))
	if !math.IsInf(got, 1) && got < 1e300 {
		t.Errorf("a ratio past float64 = %v, want +Inf or a very large value", got)
	}
}

var benchRatio float64

// The ratio the fair-sharing tournament takes per candidate, per ancestor.
// Small operands take the int64 division, large ones the exact one.
func BenchmarkPerThousandOfSmall(b *testing.B) {
	borrowed, lendable := NewAmount(1_000), NewAmount(1_000_000)
	var got float64
	for b.Loop() {
		got = borrowed.PerThousandOf(lendable)
	}
	benchRatio = got
}

func BenchmarkPerThousandOfLarge(b *testing.B) {
	borrowed := NewAmount(1_000)
	lendable := NewAmount(math.MaxInt64).AddInt64(1)
	var got float64
	for b.Loop() {
		got = borrowed.PerThousandOf(lendable)
	}
	benchRatio = got
}
