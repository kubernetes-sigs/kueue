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

package generator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

func TestLoadConfig(t *testing.T) {
	smallWl := WorkloadTemplate{
		ClassName: "small",
		RuntimeMs: 10,
		Priority:  50,
		Request:   "1",
	}

	mediumWl := WorkloadTemplate{
		ClassName: "medium",
		RuntimeMs: 50_000,
		Priority:  100,
		Request:   "5",
	}

	largeWl := WorkloadTemplate{
		ClassName: "large",
		RuntimeMs: 100_000,
		Priority:  200,
		Request:   "20",
	}

	want := []CohortSet{
		{
			ClassName: "cohort",
			Count:     5,
			QueuesSets: []QueuesSet{
				{
					ClassName:           "cq",
					Count:               6,
					NominalQuota:        "20",
					BorrowingLimit:      "100",
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					WorkloadsSets: []WorkloadsSet{
						{
							Count:              200,
							CreationIntervalMs: 100,
							Workloads: []WorkloadTemplate{
								smallWl,
								smallWl,
								smallWl,
								smallWl,
								smallWl,
								smallWl,
								smallWl,
								mediumWl,
								mediumWl,
								largeWl,
							},
						},
					},
				},
			},
		},
	}

	testContent := `
- className: cohort
  count: 5
  queuesSets:
  - className: cq
    count: 6
    nominalQuota: 20
    borrowingLimit: 100
    reclaimWithinCohort: Any
    withinClusterQueue: LowerPriority
    workloadsSets:
    - count: 200
      creationIntervalMs: 100
      workloads:
      - &small
        className: small
        runtimeMs: 10
        priority: 50
        request: 1
      - *small
      - *small
      - *small
      - *small
      - *small
      - *small
      - &medium
        className: medium
        runtimeMs: 50000
        priority: 100
        request: 5
      - *medium
      - className: large
        runtimeMs: 100000
        priority: 200
        request: 20
`
	tempDir := t.TempDir()
	fPath := filepath.Join(tempDir, "config.yaml")
	err := os.WriteFile(fPath, []byte(testContent), os.FileMode(0600))
	if err != nil {
		t.Fatalf("unable to create the test file: %s", err)
	}

	got, err := LoadConfig(fPath)
	if err != nil {
		t.Fatalf("unexpected load error: %s", err)
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("unexpected config(want-/ got+):\n%s", diff)
	}
}
