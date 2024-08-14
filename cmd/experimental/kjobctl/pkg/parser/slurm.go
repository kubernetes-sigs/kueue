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
	"slices"
	"strings"
)

const slurmDirective = "#SBATCH"

var longFlagFormat = regexp.MustCompile(`(\w+[\w+-]*)=(\S+)`)
var shortFlagFormat = regexp.MustCompile(`-(\w)\s+(\S+)`)

var slurmSupportedFlags = []string{
	"a",
	"array",
	"cpus-per-task",
	"e",
	"error",
	"gpus-per-task",
	"i",
	"input",
	"J",
	"job-name",
	"mem-per-cpu",
	"mem-per-gpu",
	"mem-per-task",
	"N",
	"nodes",
	"n",
	"ntasks",
	"o",
	"output",
	"partition",
}

func SlurmFlags(script *bufio.Scanner, ignoreUnknown bool) (map[string]string, error) {
	opts := make(map[string]string)

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

		if !slices.Contains(slurmSupportedFlags, key) {
			if ignoreUnknown {
				continue
			}
			return nil, fmt.Errorf("unknown flag: %s", key)
		}

		opts[key] = val
	}

	return opts, nil
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
