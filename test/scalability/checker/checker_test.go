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

	"sigs.k8s.io/kueue/test/scalability/runner/stats"
)

var (
	cmdStatsFile = flag.String("cmdStats", "", "command stats yaml file")
	rangeFile    = flag.String("range", "", "expectations range file")
)

type RangeSpec struct {
	Cmd struct {
		MaxWallMs int64  `json:"maxWallMs"`
		MaxUserMs int64  `json:"maxUserMs"`
		MaxSysMs  int64  `json:"maxSysMs"`
		Maxrss    uint64 `json:"maxrss"`
	} `json:"cmd"`
}

func TestScalability(t *testing.T) {
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
			t.Errorf("Wall time %dms is grater than maximum expected %dms", cmdStats.WallMs, rangeSpec.Cmd.MaxWallMs)
		}
		if cmdStats.UserMs > rangeSpec.Cmd.MaxUserMs {
			t.Errorf("User time %dms is grater than maximum expected %dms", cmdStats.UserMs, rangeSpec.Cmd.MaxUserMs)
		}
		if cmdStats.SysMs > rangeSpec.Cmd.MaxSysMs {
			t.Errorf("Sys time %dms is grater than maximum expected %dms", cmdStats.SysMs, rangeSpec.Cmd.MaxSysMs)
		}
		if cmdStats.Maxrss > int64(rangeSpec.Cmd.Maxrss) {
			t.Errorf("Maxrss %dKib is grater than maximum expected %dKib", cmdStats.Maxrss, rangeSpec.Cmd.Maxrss)
		}
	})
}
