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

package expectations

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/kueue/pkg/util/parallelize"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

type keyUIDs struct {
	key  types.NamespacedName
	uids []types.UID
}

func TestExpectations(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	initial := []keyUIDs{
		{
			key:  types.NamespacedName{Name: "g1"},
			uids: []types.UID{"a", "b", "c"},
		},
		{
			key:  types.NamespacedName{Name: "g2"},
			uids: []types.UID{"x", "y", "z"},
		},
		{
			key:  types.NamespacedName{Name: "g3"},
			uids: []types.UID{"a", "b", "c", "x", "y", "z"},
		},
	}
	expectations := NewStore("test")
	err := parallelize.Until(ctx, len(initial), func(i int) error {
		e := initial[i]
		expectations.ExpectUIDs(log, e.key, e.uids)
		return nil
	})
	if err != nil {
		t.Fatalf("Inserting initial UIDs: %v", err)
	}

	observe := []keyUIDs{
		{
			key:  types.NamespacedName{Name: "g1"},
			uids: []types.UID{"a", "b", "c"},
		},
		{
			key:  types.NamespacedName{Name: "g2"},
			uids: []types.UID{"x", "y", "z", "x", "y", "z"},
		},
		{
			key:  types.NamespacedName{Name: "g3"},
			uids: []types.UID{"a", "b", "c", "x", "y"},
		},
	}
	err = parallelize.Until(ctx, len(observe), func(i int) error {
		e := observe[i]
		return parallelize.Until(ctx, len(e.uids), func(j int) error {
			expectations.ObservedUID(log, e.key, e.uids[j])
			return nil
		})
	})
	if err != nil {
		t.Fatalf("Observing UIDs: %v", err)
	}

	wantSatisfied := map[types.NamespacedName]bool{
		{Name: "g1"}: true,
		{Name: "g2"}: true,
		{Name: "g3"}: false,
	}
	for key, want := range wantSatisfied {
		got := expectations.Satisfied(log, key)
		if got != want {
			t.Errorf("Got key %s satisfied: %t, want %t", key, got, want)
		}
	}
}

func TestCreationStore(t *testing.T) {
	log := utiltesting.NewLogger(t)
	cs := NewCreationStore("test", 0) // no expiry

	key1 := types.NamespacedName{Name: "pr1", Namespace: "ns1"}
	key2 := types.NamespacedName{Name: "pr2", Namespace: "ns2"}

	// Initially all expectations are satisfied (no outstanding creations).
	if !cs.Satisfied(log, key1) {
		t.Errorf("Expected key1 to be satisfied initially")
	}
	if !cs.Satisfied(log, key2) {
		t.Errorf("Expected key2 to be satisfied initially")
	}

	// ExpectCreation records an outstanding creation.
	cs.ExpectCreation(log, key1)
	if cs.Satisfied(log, key1) {
		t.Errorf("Expected key1 to NOT be satisfied after ExpectCreation")
	}
	if !cs.Satisfied(log, key2) {
		t.Errorf("Expected key2 to still be satisfied")
	}

	// CreationObserved clears the expectation.
	cs.CreationObserved(log, key1)
	if !cs.Satisfied(log, key1) {
		t.Errorf("Expected key1 to be satisfied after CreationObserved")
	}

	// Multiple expectations can be tracked independently.
	cs.ExpectCreation(log, key1)
	cs.ExpectCreation(log, key2)
	if cs.Satisfied(log, key1) {
		t.Errorf("Expected key1 to NOT be satisfied")
	}
	if cs.Satisfied(log, key2) {
		t.Errorf("Expected key2 to NOT be satisfied")
	}

	cs.CreationObserved(log, key1)
	if !cs.Satisfied(log, key1) {
		t.Errorf("Expected key1 to be satisfied after CreationObserved")
	}
	if cs.Satisfied(log, key2) {
		t.Errorf("Expected key2 to still NOT be satisfied")
	}

	cs.CreationObserved(log, key2)
	if !cs.Satisfied(log, key2) {
		t.Errorf("Expected key2 to be satisfied after CreationObserved")
	}
}

func TestCreationStoreResetExpired(t *testing.T) {
	log := utiltesting.NewLogger(t)
	ttl := 50 * time.Millisecond
	cs := NewCreationStore("test", ttl)

	key := types.NamespacedName{Name: "pr1", Namespace: "ns1"}

	// Record an expectation.
	cs.ExpectCreation(log, key)
	if cs.Satisfied(log, key) {
		t.Errorf("Expected key to NOT be satisfied after ExpectCreation")
	}

	// Before TTL expires, ResetExpired must retain the expectation.
	removed := cs.ResetExpired(log)
	if removed != 0 {
		t.Errorf("Expected 0 expired entries before TTL expiry, got %d", removed)
	}
	if cs.Satisfied(log, key) {
		t.Errorf("Expected key to NOT be satisfied before TTL expiry")
	}

	// Wait for TTL to expire, then reset.
	time.Sleep(100 * time.Millisecond)
	removed = cs.ResetExpired(log)
	if removed != 1 {
		t.Errorf("Expected 1 expired entry to be removed, got %d", removed)
	}
	if !cs.Satisfied(log, key) {
		t.Errorf("Expected key to be satisfied after ResetExpired")
	}

	// ResetExpired on an empty store returns 0.
	removed = cs.ResetExpired(log)
	if removed != 0 {
		t.Errorf("Expected 0 expired entries, got %d", removed)
	}
}

func TestCreationStoreConcurrentAccess(t *testing.T) {
	log := utiltesting.NewLogger(t)
	cs := NewCreationStore("test", 0)
	keys := []types.NamespacedName{
		{Name: "pr1", Namespace: "ns1"},
		{Name: "pr2", Namespace: "ns2"},
		{Name: "pr3", Namespace: "ns3"},
	}

	// Concurrent ExpectCreation + CreationObserved should not race.
	ctx := t.Context()
	err := parallelize.Until(ctx, len(keys), func(i int) error {
		key := keys[i]
		cs.ExpectCreation(log, key)
		cs.CreationObserved(log, key)
		return nil
	})
	if err != nil {
		t.Fatalf("Concurrent access: %v", err)
	}

	for _, key := range keys {
		if !cs.Satisfied(log, key) {
			t.Errorf("Expected key %s to be satisfied after concurrent Expect+Observe", key)
		}
	}
}

func TestCreationStoreNoTTL(t *testing.T) {
	log := utiltesting.NewLogger(t)
	cs := NewCreationStore("test", 0) // zero TTL means no expiry

	key := types.NamespacedName{Name: "pr1", Namespace: "ns1"}
	cs.ExpectCreation(log, key)

	// With zero TTL, ResetExpired should never remove entries.
	time.Sleep(100 * time.Millisecond)
	removed := cs.ResetExpired(log)
	if removed != 0 {
		t.Errorf("Expected 0 removed entries with zero TTL, got %d", removed)
	}
	if cs.Satisfied(log, key) {
		t.Errorf("Expected key to NOT be satisfied (zero TTL means no auto-expiry)")
	}
}
