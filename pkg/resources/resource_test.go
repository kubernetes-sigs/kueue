package resources

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestSafeAdd(t *testing.T) {
	var a FlavorResourceQuantities = nil
	fr := FlavorResource{Flavor: "Hello", Resource: "World"}

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Panic while assigning to nil map")
		}
	}()
	a.Add(fr, 5)
	a.Add(fr, -7)

	expected := FlavorResourceQuantitiesFlat{fr: -2}.Unflatten()
	if diff := cmp.Diff(a, expected); diff != "" {
		t.Fatalf("Unexpected diff %s", diff)
	}
}

func TestFor(t *testing.T) {
	var a FlavorResourceQuantities = nil

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Panic while accessing nil map")
		}
	}()
	result := a.For(FlavorResource{Flavor: "Hello", Resource: "World"})

	if result != 0 {
		t.Fatalf("Unexpected result: %d != 0", result)
	}
}
