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

package checker

import (
	"flag"
	"os"
	"testing"

	"sigs.k8s.io/yaml"

	"sigs.k8s.io/kueue/test/performance/scheduler/runner/recorder"
	"sigs.k8s.io/kueue/test/performance/scheduler/runner/stats"
)

var (
	summaryFile  = flag.String("summary", "", "the runner summary report")
	cmdStatsFile = flag.String("cmdStats", "", "command stats yaml file")
	rangeFile    = flag.String("range", "", "expectations range file")
)

type RangeSpec struct {
	Cmd struct {
		MaxWallMs int64  `json:"maxWallMs"`
		MCPU      int64  `json:"mCPU"`
		Maxrss    uint64 `json:"maxrss"`
	} `json:"cmd"`
	ClusterQueueClassesMinUsage      map[string]float64 `json:"clusterQueueClassesMinUsage"`
	WlClassesMaxAvgTimeToAdmissionMs map[string]int64   `json:"wlClassesMaxAvgTimeToAdmissionMs"`
}

func TestScalability(t *testing.T) {
	summaryBytes, err := os.ReadFile(*summaryFile)
	if err != nil {
		t.Fatalf("Unable to read summary: %s", err)
	}

	summary := recorder.Summary{}

	err = yaml.UnmarshalStrict(summaryBytes, &summary)
	if err != nil {
		t.Fatalf("Unable to unmarshal summary: %s", err)
	}

	cmdStatsBytes, err := os.ReadFile(*cmdStatsFile)
	if err != nil {
		t.Fatalf("Unable to read command stats: %s", err)
	}

	cmdStats := stats.CmdStats{}
	err = yaml.UnmarshalStrict(cmdStatsBytes, &cmdStats)
	if err != nil {
		t.Fatalf("Unable to unmarshal command stats: %s", err)
	}

	rangeBytes, err := os.ReadFile(*rangeFile)
	if err != nil {
		t.Fatalf("Unable to read range spec: %s", err)
	}

	rangeSpec := RangeSpec{}
	err = yaml.UnmarshalStrict(rangeBytes, &rangeSpec)
	if err != nil {
		t.Fatalf("Unable to unmarshal range spec: %s", err)
	}

	t.Run("CommandStats", func(t *testing.T) {
		if cmdStats.WallMs > rangeSpec.Cmd.MaxWallMs {
			t.Errorf("Wall time %dms is greater than maximum expected %dms", cmdStats.WallMs, rangeSpec.Cmd.MaxWallMs)
		}
		mCPUUsed := (cmdStats.SysMs + cmdStats.UserMs) * 1000 / cmdStats.WallMs
		if mCPUUsed > rangeSpec.Cmd.MCPU {
			t.Errorf("Average CPU usage %dmCpu is greater than maximum expected %dmCPU", mCPUUsed, rangeSpec.Cmd.MCPU)
		}
		if cmdStats.Maxrss > int64(rangeSpec.Cmd.Maxrss) {
			t.Errorf("Maxrss %dKib is greater than maximum expected %dKib", cmdStats.Maxrss, rangeSpec.Cmd.Maxrss)
		}
	})

	t.Run("ClusterQueueClasses", func(t *testing.T) {
		for c, cqcSummary := range summary.ClusterQueueClasses {
			t.Run(c, func(t *testing.T) {
				expected, found := rangeSpec.ClusterQueueClassesMinUsage[c]
				if !found {
					t.Fatalf("Unexpected class")
				}
				actual := float64(cqcSummary.CPUUsed) * 100 / (float64(cqcSummary.NominalQuota) * float64(cqcSummary.LastEventTime.Sub(cqcSummary.FirstEventTime).Milliseconds()))
				if actual < expected {
					t.Errorf("Usage %.2f%% is less then expected %.2f%%", actual, expected)
				}
			})
		}
	})

	t.Run("WorkloadClasses", func(t *testing.T) {
		for c, wlcSummary := range summary.WorkloadClasses {
			t.Run(c, func(t *testing.T) {
				expected, found := rangeSpec.WlClassesMaxAvgTimeToAdmissionMs[c]
				if !found {
					t.Fatalf("Unexpected class")
				}
				if wlcSummary.AverageTimeToAdmissionMs > expected {
					t.Errorf("Average wait for admission %dms is more then expected %dms", wlcSummary.AverageTimeToAdmissionMs, expected)
				}
			})
		}
	})
}
