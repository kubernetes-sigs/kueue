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
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseSlurmOptions(t *testing.T) {
	testCases := map[string]struct {
		script        string
		ignoreUnknown bool
		want          map[string]string
		wantErr       string
	}{
		"should parse simple script": {
			script: `#!/bin/bash
#SBATCH --job-name=single_Cpu
#SBATCH --ntasks=1
#SBATCH --cpus-per-task=1

sleep 30
echo "hello"`,
			want: map[string]string{
				"job-name":      "single_Cpu",
				"ntasks":        "1",
				"cpus-per-task": "1",
			},
		},
		"should parse script with short flags": {
			script: `#SBATCH -J serial_Test_Job
#SBATCH -n 1
#SBATCH -o output.%j
#SBATCH -e error.%j

./myexec
exit 0`,
			want: map[string]string{
				"J": "serial_Test_Job",
				"n": "1",
				"o": "output.%j",
				"e": "error.%j",
			},
		},
		"should parse script with comments": {
			script: `#!/bin/bash
# Job name
#SBATCH --job-name=job-array
# Defines a job array from task ID 1 to 20
#SBATCH --array=1-20
# Number of tasks (in this case, one task per array element)
#SBATCH -n 1
# Partition or queue name
#SBATCH --partition=shared                      
#SBATCH                           # This is an empty line to separate Slurm directives from the job commands

echo "Start Job $SLURM_ARRAY_TASK_ID on $HOSTNAME"  # Display job start information

sleep 10`,
			want: map[string]string{
				"job-name":  "job-array",
				"array":     "1-20",
				"n":         "1",
				"partition": "shared",
			},
		},
		"should parse script and ignore unknown flags": {
			script: `#!/bin/bash
#SBATCH --job-name=my_job_name
#SBATCH --output=output.txt
#SBATCH --error=error.txt
#SBATCH --partition=partition_name
#SBATCH --nodes=1
#SBATCH --ntasks=1
#SBATCH --cpus-per-task=1
#SBATCH --time=1:00:00
#SBATCH --mail-type=END
#SBATCH --mail-user=your@email.com

python my_script.py`,
			ignoreUnknown: true,
			want: map[string]string{
				"job-name":      "my_job_name",
				"output":        "output.txt",
				"error":         "error.txt",
				"partition":     "partition_name",
				"nodes":         "1",
				"ntasks":        "1",
				"cpus-per-task": "1",
			},
		},
		"should fail due to unknown flags": {
			script: `#!/bin/bash
#SBATCH --job-name=my_job_name
#SBATCH --nodes=1
#SBATCH --cpus-per-task=1
#SBATCH --time=1:00:00

python my_script.py`,
			wantErr: "unknown flag: time",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			script := bufio.NewScanner(strings.NewReader(tc.script))
			got, gotErr := SlurmFlags(script, tc.ignoreUnknown)

			var gotErrStr string
			if gotErr != nil {
				gotErrStr = gotErr.Error()
			}
			if diff := cmp.Diff(tc.wantErr, gotErrStr); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
				return
			}

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected options (-want/+got)\n%s", diff)
			}
		})
	}
}
