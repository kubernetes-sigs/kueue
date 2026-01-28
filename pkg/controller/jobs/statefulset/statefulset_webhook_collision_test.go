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

package statefulset

import (
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

func TestWorkloadNameUniqueWithGenerateName(t *testing.T) {
	// Simulate two StatefulSets created from the same YAML with generateName
	// After creation, they have different UIDs but the same name prefix
	sts1UID := types.UID("uid-sts1-abc123")
	sts2UID := types.UID("uid-sts2-def456")

	// Both StatefulSets have the same generated name pattern
	// (in reality, after creation they'd have names like "sleep-abc12" and "sleep-def45")
	sts1Name := "sleep-abc12"
	sts2Name := "sleep-def45"

	// Generate workload names using the PrebuiltWorkload pattern (UID-based)
	workloadName1 := GetWorkloadName(sts1UID, sts1Name)
	workloadName2 := GetWorkloadName(sts2UID, sts2Name)

	t.Logf("sts1 workloadName: %s", workloadName1)
	t.Logf("sts2 workloadName: %s", workloadName2)

	// Workload names MUST be different since UIDs are different
	if workloadName1 == workloadName2 {
		t.Errorf("Collision detected: both StatefulSets got same workload name: %s", workloadName1)
	} else {
		t.Log("Success: Workload names are different due to unique UIDs")
	}
}

func TestWorkloadNameCollisionWithEmptyUID(t *testing.T) {
	// This test demonstrates the original bug when UID was empty (webhook time)
	emptyUID := types.UID("")

	// Two StatefulSets with generateName (empty Name at webhook time)
	// With the old implementation, this caused collision
	name1 := "" // Empty at webhook time
	name2 := "" // Empty at webhook time

	workloadName1 := GetWorkloadName(emptyUID, name1)
	workloadName2 := GetWorkloadName(emptyUID, name2)

	t.Logf("Empty name workloadName1: %s", workloadName1)
	t.Logf("Empty name workloadName2: %s", workloadName2)

	// With empty UID and name, they will be the same (this is expected)
	// The fix is that we don't use this at webhook time anymore
	if workloadName1 != workloadName2 {
		t.Errorf("Unexpected: workload names should be same with empty UID and name")
	} else {
		t.Log("Expected: Same workload name with empty UID - this is why we use PrebuiltWorkload pattern")
	}
}
