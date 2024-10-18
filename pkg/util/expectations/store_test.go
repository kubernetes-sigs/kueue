/*
Copyright 2024 The Kubernetes Authors.

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
