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
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
)

func TestParseSlurmOptions(t *testing.T) {
	testCases := map[string]struct {
		script        string
		ignoreUnknown bool
		want          ParsedSlurmFlags
		wantErr       string
	}{
		"should parse simple script": {
			script: `#!/bin/bash
#SBATCH --job-name=single_Cpu
#SBATCH --ntasks=1
#SBATCH --cpus-per-task=1

sleep 30
echo "hello"`,
			want: ParsedSlurmFlags{
				JobName:     "single_Cpu",
				NTasks:      ptr.To[int32](1),
				CpusPerTask: ptr.To(resource.MustParse("1")),
			},
		},
		"should parse script with short flags": {
			script: `#SBATCH -J serial_Test_Job
#SBATCH -n 1
#SBATCH -o output.%j
#SBATCH -e error.%j

./myexec
exit 0`,
			want: ParsedSlurmFlags{
				JobName: "serial_Test_Job",
				NTasks:  ptr.To[int32](1),
				Output:  "output.%j",
				Error:   "error.%j",
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
			want: ParsedSlurmFlags{
				JobName:   "job-array",
				Array:     "1-20",
				NTasks:    ptr.To[int32](1),
				Partition: "shared",
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
#SBATCH --unknown=unknown

python my_script.py`,
			ignoreUnknown: true,
			want: ParsedSlurmFlags{
				JobName:     "my_job_name",
				Output:      "output.txt",
				Error:       "error.txt",
				Partition:   "partition_name",
				Nodes:       ptr.To[int32](1),
				NTasks:      ptr.To[int32](1),
				CpusPerTask: ptr.To(resource.MustParse("1")),
				TimeLimit:   "1:00:00",
			},
		},
		"should fail due to unknown flags": {
			script: `#!/bin/bash
#SBATCH --job-name=my_job_name
#SBATCH --nodes=1
#SBATCH --cpus-per-task=1
#SBATCH --unknown=unknown

python my_script.py`,
			wantErr: "unknown flag: unknown",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got, gotErr := SlurmFlags(tc.script, tc.ignoreUnknown)

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
