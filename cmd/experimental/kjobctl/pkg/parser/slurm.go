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

package parser

import (
	"bufio"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
)

const slurmDirective = "#SBATCH"

var longFlagFormat = regexp.MustCompile(`(\w+[\w+-]*)=(\S+)`)
var shortFlagFormat = regexp.MustCompile(`-(\w)\s+(\S+)`)

type ParsedSlurmFlags struct {
	Array       string
	CpusPerTask *resource.Quantity
	GpusPerTask *resource.Quantity
	MemPerTask  *resource.Quantity
	MemPerCPU   *resource.Quantity
	MemPerGPU   *resource.Quantity
	Nodes       *int32
	NTasks      *int32
	StdOut      string
	StdErr      string
	Input       string
	JobName     string
	Partition   string
}

func SlurmFlags(script *bufio.Scanner, ignoreUnknown bool) (ParsedSlurmFlags, error) {
	var flags ParsedSlurmFlags

	for script.Scan() {
		line := strings.TrimSpace(script.Text())
		if len(line) == 0 {
			continue
		}

		if !strings.HasPrefix(line, slurmDirective) {
			if strings.HasPrefix(line, "#") {
				continue
			}
			break
		}

		key, val := extractKeyValue(line)
		if len(key) == 0 || len(val) == 0 {
			continue
		}

		switch key {
		case "a", string(v1alpha1.ArrayFlag):
			flags.Array = val
		case string(v1alpha1.CpusPerTaskFlag):
			flags.CpusPerTask = ptr.To(resource.MustParse(val))
		case "e", string(v1alpha1.StdErrFlag):
			flags.StdErr = val
		case string(v1alpha1.GpusPerTaskFlag):
			flags.GpusPerTask = ptr.To(resource.MustParse(val))
		case "i", string(v1alpha1.InputFlag):
			flags.Input = val
		case "J", string(v1alpha1.JobNameFlag):
			flags.JobName = val
		case string(v1alpha1.MemPerCPUFlag):
			flags.MemPerCPU = ptr.To(resource.MustParse(val))
		case string(v1alpha1.MemPerGPUFlag):
			flags.MemPerGPU = ptr.To(resource.MustParse(val))
		case string(v1alpha1.MemPerTaskFlag):
			flags.MemPerTask = ptr.To(resource.MustParse(val))
		case "N", string(v1alpha1.NodesFlag):
			intVal, err := strconv.ParseInt(val, 10, 32)
			if err != nil {
				return flags, err
			}
			flags.Nodes = ptr.To(int32(intVal))
		case "n", string(v1alpha1.NTasksFlag):
			intVal, err := strconv.ParseInt(val, 10, 32)
			if err != nil {
				return flags, err
			}
			flags.NTasks = ptr.To(int32(intVal))
		case "o", string(v1alpha1.StdOutFlag):
			flags.StdOut = val
		case "p", string(v1alpha1.PartitionFlag):
			flags.Partition = val
		default:
			if !ignoreUnknown {
				return ParsedSlurmFlags{}, fmt.Errorf("unknown flag: %s", key)
			}
		}
	}

	return flags, nil
}

func extractKeyValue(s string) (string, string) {
	matches := longFlagFormat.FindStringSubmatch(s)

	if len(matches) != 3 {
		matches = shortFlagFormat.FindStringSubmatch(s)
		if len(matches) != 3 {
			return "", ""
		}
	}

	return matches[1], matches[2]
}
