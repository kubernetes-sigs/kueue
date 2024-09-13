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

package main

import (
	"cmp"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"time"

	gtypes "github.com/onsi/ginkgo/v2/types"
	"gopkg.in/yaml.v3"
)

type SpecDescription struct {
	Suite              string
	ContainerHierarchy []string
	Name               string
	Duration           time.Duration
	Events             []Event
}

type Event struct {
	Message  string
	Location string
	Duration time.Duration
}

var (
	reportPath = flag.String("i", "ginkgo.json", "input file name")
	maxCount   = flag.Uint("maxCount", 0, "maximum number of items")
	withEvents = flag.Bool("withEvents", true, "show events")
)

func main() {
	flag.Parse()
	reprtData, err := os.ReadFile(*reportPath)
	if err != nil {
		panic(err)
	}

	report := []gtypes.Report{}
	err = json.Unmarshal(reprtData, &report)
	if err != nil {
		panic(err)
	}

	flattenSpecs := []SpecDescription{}
	for _, r := range report {
		for _, s := range r.SpecReports {
			sd := SpecDescription{
				Suite:              r.SuiteDescription,
				ContainerHierarchy: s.ContainerHierarchyTexts,
				Name:               s.LeafNodeText,
				Duration:           s.RunTime,
			}
			if *withEvents {
				for _, ev := range s.SpecEvents {
					if ev.Duration > 0 {
						event := Event{
							Location: ev.CodeLocation.String(),
							Message:  fmt.Sprintf("%s (%s)", ev.Message, ev.NodeType),
							Duration: ev.Duration,
						}
						sd.Events = append(sd.Events, event)
					}
				}
				slices.SortFunc(sd.Events, func(a, b Event) int {
					return cmp.Compare(b.Duration, a.Duration)
				})
			}
			flattenSpecs = append(flattenSpecs, sd)
		}
	}
	slices.SortFunc(flattenSpecs, func(a, b SpecDescription) int {
		return cmp.Compare(b.Duration, a.Duration)
	})

	yout := yaml.NewEncoder(os.Stdout)
	if *maxCount > 0 {
		yout.Encode(flattenSpecs[:min(int(*maxCount), len(flattenSpecs))])
	} else {
		yout.Encode(flattenSpecs)
	}
}
