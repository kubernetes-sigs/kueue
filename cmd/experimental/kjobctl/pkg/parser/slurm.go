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
	"errors"
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
	GpusPerTask map[string]*resource.Quantity
	MemPerNode  *resource.Quantity
	MemPerTask  *resource.Quantity
	MemPerCPU   *resource.Quantity
	MemPerGPU   *resource.Quantity
	Nodes       *int32
	NTasks      *int32
	Output      string
	Error       string
	Input       string
	JobName     string
	Partition   string
	TimeLimit   string
}

func SlurmFlags(script string, ignoreUnknown bool) (ParsedSlurmFlags, error) {
	var flags ParsedSlurmFlags

	scanner := bufio.NewScanner(strings.NewReader(script))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
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
			cpusPerTask, err := resource.ParseQuantity(val)
			if err != nil {
				return flags, fmt.Errorf("cannot parse '%s': %w", val, err)
			}
			flags.CpusPerTask = ptr.To(cpusPerTask)
		case "e", string(v1alpha1.ErrorFlag):
			flags.Error = val
		case string(v1alpha1.GpusPerTaskFlag):
			gpusPerTask, err := GpusFlag(val)
			if err != nil {
				return flags, fmt.Errorf("cannot parse '%s': %w", val, err)
			}
			flags.GpusPerTask = gpusPerTask
		case "i", string(v1alpha1.InputFlag):
			flags.Input = val
		case "J", string(v1alpha1.JobNameFlag):
			flags.JobName = val
		case string(v1alpha1.MemPerNodeFlag):
			memPerNode, err := resource.ParseQuantity(val)
			if err != nil {
				return flags, fmt.Errorf("cannot parse '%s': %w", val, err)
			}
			flags.MemPerNode = ptr.To(memPerNode)
		case string(v1alpha1.MemPerCPUFlag):
			memPerCPU, err := resource.ParseQuantity(val)
			if err != nil {
				return flags, fmt.Errorf("cannot parse '%s': %w", val, err)
			}
			flags.MemPerCPU = ptr.To(memPerCPU)
		case string(v1alpha1.MemPerGPUFlag):
			memPerGPU, err := resource.ParseQuantity(val)
			if err != nil {
				return flags, fmt.Errorf("cannot parse '%s': %w", val, err)
			}
			flags.MemPerGPU = ptr.To(memPerGPU)
		case string(v1alpha1.MemPerTaskFlag):
			memPerTask, err := resource.ParseQuantity(val)
			if err != nil {
				return flags, fmt.Errorf("cannot parse '%s': %w", val, err)
			}
			flags.MemPerTask = ptr.To(memPerTask)
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
		case "o", string(v1alpha1.OutputFlag):
			flags.Output = val
		case "p", string(v1alpha1.PartitionFlag):
			flags.Partition = val
		case "t", string(v1alpha1.TimeFlag):
			flags.TimeLimit = val
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

func GpusFlag(val string) (map[string]*resource.Quantity, error) {
	gpus := make(map[string]*resource.Quantity)

	items := strings.Split(val, ",")
	for _, v := range items {
		gpu := strings.Split(v, ":")
		if len(gpu) != 2 {
			return nil, errors.New("invalid GPU format. It must be <type>:<number>")
		}

		name := gpu[0]
		quantity, err := resource.ParseQuantity(gpu[1])
		if err != nil {
			return nil, err
		}

		gpus[name] = &quantity
	}

	return gpus, nil
}
