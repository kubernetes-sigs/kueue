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

package builder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	kjobctlfake "sigs.k8s.io/kueue/cmd/experimental/kjobctl/client-go/clientset/versioned/fake"
	cmdtesting "sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/testing"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/testing/wrappers"
)

func TestUnmaskFilenameFunction(t *testing.T) {
	testCases := map[string]struct {
		input              string
		slurmArrayJobID    int32
		slurmJobID         int32
		hostname           string
		jobCompletionIndex int32
		slurmArrayTaskID   int32
		userID             string
		sbatchJobName      string
		wantOut            string
	}{
		"should not replace": {
			input:   "filename.txt",
			wantOut: "filename.txt\n",
		},
		"should not process any of the replacement symbols": {
			input:   "\\filename-%u.txt",
			userID:  "test",
			wantOut: "filename-%u.txt\n",
		},
		"should replace": {
			input:              "filename-%%-%A-%a-%j-%N-%n-%t-%u-%x.txt",
			slurmArrayJobID:    1,
			slurmJobID:         2,
			hostname:           "host",
			slurmArrayTaskID:   4,
			jobCompletionIndex: 3,
			userID:             "test",
			sbatchJobName:      "name",
			wantOut:            "filename-%-1-4-2-host-3-4-test-name.txt\n",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			file, err := os.CreateTemp("", "slurm-*.sh")
			if err != nil {
				t.Error(err)
				return
			}

			script := fmt.Sprintf(`#!/usr/bin/bash

set -o errexit
set -o nounset
set -o pipefail

%s

unmask_filename "%s"

`, unmaskFilenameFunction, tc.input)

			if _, err := file.WriteString(script); err != nil {
				t.Error(err)
				return
			}

			if err := file.Close(); err != nil {
				t.Error(err)
				return
			}

			defer os.Remove(file.Name())

			envs := []string{
				fmt.Sprintf("SLURM_ARRAY_JOB_ID=%d", tc.slurmArrayJobID),
				fmt.Sprintf("SLURM_ARRAY_TASK_ID=%d", tc.slurmArrayTaskID),
				fmt.Sprintf("SLURM_JOB_ID=%d", tc.slurmJobID),
				fmt.Sprintf("HOSTNAME=%s", tc.hostname),
				fmt.Sprintf("JOB_COMPLETION_INDEX=%d", tc.jobCompletionIndex),
				fmt.Sprintf("SLURM_ARRAY_TASK_ID=%d", tc.slurmArrayTaskID),
				fmt.Sprintf("USER_ID=%s", tc.userID),
				fmt.Sprintf("SBATCH_JOB_NAME=%s", tc.sbatchJobName),
			}

			cmd := exec.Command("bash", file.Name())
			cmd.Env = os.Environ()
			cmd.Env = append(cmd.Env, envs...)

			gotOut, err := cmd.CombinedOutput()
			if err != nil {
				t.Error(err)
				return
			}

			if diff := cmp.Diff(tc.wantOut, string(gotOut)); diff != "" {
				t.Errorf("String (-want,+got):\n%s", diff)
			}
		})
	}
}

type slurmBuilderTestCase struct {
	beforeTest  func(tc *slurmBuilderTestCase) error
	afterTest   func(tc *slurmBuilderTestCase) error
	tempFile    string
	namespace   string
	profile     string
	mode        v1alpha1.ApplicationProfileMode
	array       string
	cpusPerTask *resource.Quantity
	gpusPerTask *resource.Quantity
	memPerTask  *resource.Quantity
	memPerCPU   *resource.Quantity
	memPerGPU   *resource.Quantity
	nodes       *int32
	nTasks      *int32
	output      string
	err         string
	input       string
	jobName     string
	partition   string
	kjobctlObjs []runtime.Object
	wantObj     []runtime.Object
	wantErr     error
	cmpopts     []cmp.Option
}

func beforeSlurmTest(tc *slurmBuilderTestCase) error {
	file, err := os.CreateTemp("", "slurm")
	if err != nil {
		return err
	}
	defer file.Close()

	if _, err := file.WriteString("#!/bin/bash\nsleep 300'"); err != nil {
		return err
	}

	tc.tempFile = file.Name()

	return nil
}

func afterSlurmTest(tc *slurmBuilderTestCase) error {
	return os.Remove(tc.tempFile)
}

func TestSlurmBuilderDo(t *testing.T) {
	testStartTime := time.Now()
	userID := os.Getenv(constants.SystemEnvVarNameUser)

	testCases := map[string]slurmBuilderTestCase{
		"shouldn't build slurm job because template not found": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.SlurmMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{Name: v1alpha1.SlurmMode, Template: "slurm-template"}).
					Obj(),
			},
			wantErr: apierrors.NewNotFound(schema.GroupResource{Group: "kjobctl.x-k8s.io", Resource: "jobtemplates"}, "slurm-template"),
		},
		"should build slurm job": {
			beforeTest: beforeSlurmTest,
			afterTest:  afterSlurmTest,
			namespace:  metav1.NamespaceDefault,
			profile:    "profile",
			mode:       v1alpha1.SlurmMode,
			array:      "1-5%2",
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("slurm-job-template", metav1.NamespaceDefault).
					WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").Obj()).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.SlurmMode, "slurm-job-template").Obj()).
					Obj(),
			},
			wantObj: []runtime.Object{
				wrappers.MakeJob("", metav1.NamespaceDefault).
					Parallelism(2).
					Completions(5).
					CompletionMode(batchv1.IndexedCompletion).
					Profile("profile").
					WithContainer(*wrappers.MakeContainer("c1-0", "bash:4.4").
						Command("bash", "/slurm/entrypoint.sh").
						WithVolumeMount(corev1.VolumeMount{MountPath: "/slurm"}).
						Obj()).
					WithVolume(corev1.Volume{
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								Items: []corev1.KeyToPath{
									{Key: "entrypoint.sh", Path: "entrypoint.sh"},
									{Key: "script.sh", Path: "script.sh"},
								},
							},
						},
					}).
					WithEnvVar(corev1.EnvVar{Name: constants.EnvVarNameUserID, Value: userID}).
					WithEnvVar(corev1.EnvVar{Name: constants.EnvVarTaskName, Value: "default_profile"}).
					WithEnvVar(corev1.EnvVar{
						Name:  constants.EnvVarTaskID,
						Value: fmt.Sprintf("%s_%s_default_profile", userID, testStartTime.Format(time.RFC3339)),
					}).
					WithEnvVar(corev1.EnvVar{Name: "PROFILE", Value: "default_profile"}).
					WithEnvVar(corev1.EnvVar{Name: "TIMESTAMP", Value: testStartTime.Format(time.RFC3339)}).
					WithEnvVar(corev1.EnvVar{Name: "JOB_CONTAINER_INDEX", Value: "0"}).
					Obj(),
				wrappers.MakeConfigMap("", metav1.NamespaceDefault).
					Profile("profile").
					Data(map[string]string{
						"script.sh": "#!/bin/bash\nsleep 300'",
						"entrypoint.sh": `#!/usr/bin/bash

set -o errexit
set -o nounset
set -o pipefail

# External variables
# JOB_COMPLETION_INDEX  - completion index of the job.
# JOB_CONTAINER_INDEX   - container index in the container template.

# ["COMPLETION_INDEX"]="CONTAINER_INDEX_1,CONTAINER_INDEX_2"
declare -A array_indexes=(["0"]="1" ["1"]="2" ["2"]="3" ["3"]="4" ["4"]="5") 	# Requires bash v4+

container_indexes=${array_indexes[${JOB_COMPLETION_INDEX}]}
container_indexes=(${container_indexes//,/ })

if [[ ! -v container_indexes[${JOB_CONTAINER_INDEX}] ]];
then
	exit 0
fi

SBATCH_INPUT= 		# Instruct Slurm to connect the batch script's standard input directly to the file name specified in the "filename pattern".
SBATCH_OUTPUT=		# Instruct Slurm to connect the batch script's standard output directly to the file name specified in the "filename pattern".
SBATCH_ERROR=		# Instruct Slurm to connect the batch script's standard error directly to the file name specified in the "filename pattern".
SBATCH_JOB_NAME=	# Specify a name for the job allocation.

export SLURM_ARRAY_JOB_ID=1       		# Job array’s master job ID number.
export SLURM_ARRAY_TASK_COUNT=5  		# Total number of tasks in a job array.
export SLURM_ARRAY_TASK_MAX=5    		# Job array’s maximum ID (index) number.
export SLURM_ARRAY_TASK_MIN=1    		# Job array’s minimum ID (index) number.
export SLURM_TASKS_PER_NODE=1    		# Job array’s master job ID number.
export SLURM_CPUS_PER_TASK=       		# Number of CPUs per task.
export SLURM_CPUS_ON_NODE=        		# Number of CPUs on the allocated node (actually pod).
export SLURM_JOB_CPUS_PER_NODE=   		# Count of processors available to the job on this node.
export SLURM_CPUS_PER_GPU=        		# Number of CPUs requested per allocated GPU.
export SLURM_MEM_PER_CPU=         	# Memory per CPU. Same as --mem-per-cpu .
export SLURM_MEM_PER_GPU=         	# Memory per GPU.
export SLURM_MEM_PER_NODE=        	# Memory per node. Same as --mem.
export SLURM_GPUS=                	# Number of GPUs requested (in total).
export SLURM_NTASKS=1              	# Same as -n, –ntasks. The number of tasks.
export SLURM_NTASKS_PER_NODE=1  		# Number of tasks requested per node.
export SLURM_NPROCS=$SLURM_NTASKS       	# Same as -n, --ntasks. See $SLURM_NTASKS.
export SLURM_NNODES=1            		# Total number of nodes (actually pods) in the job’s resource allocation.
export SLURM_SUBMIT_DIR=/slurm        		# The path of the job submission directory.
export SLURM_SUBMIT_HOST=$HOSTNAME       	# The hostname of the node used for job submission.

export SLURM_JOB_ID=$(( JOB_COMPLETION_INDEX * SLURM_TASKS_PER_NODE + JOB_CONTAINER_INDEX + SLURM_ARRAY_JOB_ID ))   # The Job ID.
export SLURM_JOBID=$SLURM_JOB_ID                                                                                    # Deprecated. Same as $SLURM_JOB_ID
export SLURM_ARRAY_TASK_ID=${container_indexes[${JOB_CONTAINER_INDEX}]}												# Task ID.

unmask_filename () {
  replaced="$1"

  if [[ "$replaced" == "\\"* ]]; then
      replaced="${replaced//\\/}"
      echo "${replaced}"
      return 0
  fi

  replaced=$(echo "$replaced" | sed -E "s/(%)(%A)/\1\n\2/g;:a s/(^|[^\n])%A/\1$SLURM_ARRAY_JOB_ID/;ta;s/\n//g")
  replaced=$(echo "$replaced" | sed -E "s/(%)(%a)/\1\n\2/g;:a s/(^|[^\n])%a/\1$SLURM_ARRAY_TASK_ID/;ta;s/\n//g")
  replaced=$(echo "$replaced" | sed -E "s/(%)(%j)/\1\n\2/g;:a s/(^|[^\n])%j/\1$SLURM_JOB_ID/;ta;s/\n//g")
  replaced=$(echo "$replaced" | sed -E "s/(%)(%N)/\1\n\2/g;:a s/(^|[^\n])%N/\1$HOSTNAME/;ta;s/\n//g")
  replaced=$(echo "$replaced" | sed -E "s/(%)(%n)/\1\n\2/g;:a s/(^|[^\n])%n/\1$JOB_COMPLETION_INDEX/;ta;s/\n//g")
  replaced=$(echo "$replaced" | sed -E "s/(%)(%t)/\1\n\2/g;:a s/(^|[^\n])%t/\1$SLURM_ARRAY_TASK_ID/;ta;s/\n//g")
  replaced=$(echo "$replaced" | sed -E "s/(%)(%u)/\1\n\2/g;:a s/(^|[^\n])%u/\1$USER_ID/;ta;s/\n//g")
  replaced=$(echo "$replaced" | sed -E "s/(%)(%x)/\1\n\2/g;:a s/(^|[^\n])%x/\1$SBATCH_JOB_NAME/;ta;s/\n//g")

  replaced="${replaced//%%/%}"

  echo "$replaced"
}

input_file=$(unmask_filename "$SBATCH_INPUT")
output_file=$(unmask_filename "$SBATCH_OUTPUT")
error_path=$(unmask_filename "$SBATCH_ERROR")

bash /slurm/script.sh
`,
					}).
					Obj(),
			},
			cmpopts: []cmp.Option{
				cmpopts.IgnoreFields(corev1.Volume{}, "Name"),
				cmpopts.IgnoreFields(corev1.LocalObjectReference{}, "Name"),
				cmpopts.IgnoreFields(corev1.VolumeMount{}, "Name"),
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if tc.beforeTest != nil {
				if err := tc.beforeTest(&tc); err != nil {
					t.Error(err)
					return
				}
			}

			if tc.afterTest != nil {
				defer func() {
					if err := tc.afterTest(&tc); err != nil {
						t.Error(err)
					}
				}()
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			tcg := cmdtesting.NewTestClientGetter().
				WithKjobctlClientset(kjobctlfake.NewSimpleClientset(tc.kjobctlObjs...))

			gotObjs, gotErr := NewBuilder(tcg, testStartTime).
				WithNamespace(tc.namespace).
				WithProfileName(tc.profile).
				WithModeName(tc.mode).
				WithScript(tc.tempFile).
				WithArray(tc.array).
				WithCpusPerTask(tc.cpusPerTask).
				WithGpusPerTask(tc.gpusPerTask).
				WithMemPerTask(tc.memPerTask).
				WithMemPerCPU(tc.memPerCPU).
				WithMemPerGPU(tc.memPerGPU).
				WithNodes(tc.nodes).
				WithNTasks(tc.nTasks).
				WithOutput(tc.output).
				WithError(tc.err).
				WithInput(tc.input).
				WithJobName(tc.jobName).
				WithPartition(tc.partition).
				Do(ctx)

			var opts []cmp.Option
			var statusError *apierrors.StatusError
			if !errors.As(tc.wantErr, &statusError) {
				opts = append(opts, cmpopts.EquateErrors())
			}
			if diff := cmp.Diff(tc.wantErr, gotErr, opts...); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
				return
			}

			defaultCmpOpts := []cmp.Option{cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Name")}
			opts = append(defaultCmpOpts, tc.cmpopts...)
			if diff := cmp.Diff(tc.wantObj, gotObjs, opts...); diff != "" {
				t.Errorf("Objects after build (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestSlurmBuilderBuildEntrypointCommand(t *testing.T) {
	testStartTime := time.Now()

	testCases := map[string]struct {
		input                 string
		output                string
		error                 string
		wantEntrypointCommand string
	}{
		"should build entrypoint command": {
			wantEntrypointCommand: "bash /slurm/script.sh",
		},
		"should build entrypoint command with output": {
			output:                "/home/test/stdout.out",
			wantEntrypointCommand: "bash /slurm/script.sh 1>$output_file",
		},
		"should build entrypoint command with error": {
			error:                 "/home/test/stderr.out",
			wantEntrypointCommand: "bash /slurm/script.sh 2>$error_file",
		},
		"should build entrypoint command with output and error": {
			output:                "/home/test/stdout.out",
			error:                 "/home/test/stderr.out",
			wantEntrypointCommand: "bash /slurm/script.sh 1>$output_file 2>$error_file",
		},
		"should build entrypoint command with input": {
			input:                 "/home/test/script.sh",
			wantEntrypointCommand: "bash /slurm/script.sh <$input_file",
		},
		"should build entrypoint command with input and output": {
			input:                 "/home/test/script.sh",
			output:                "/home/test/stdout.out",
			wantEntrypointCommand: "bash /slurm/script.sh <$input_file 1>$output_file",
		},
		"should build entrypoint command with input and error": {
			input:                 "/home/test/script.sh",
			error:                 "/home/test/stderr.out",
			wantEntrypointCommand: "bash /slurm/script.sh <$input_file 2>$error_file",
		},
		"should build entrypoint command with input, output and error": {
			input:                 "/home/test/script.sh",
			output:                "/home/test/stdout.out",
			error:                 "/home/test/stderr.out",
			wantEntrypointCommand: "bash /slurm/script.sh <$input_file 1>$output_file 2>$error_file",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			tcg := cmdtesting.NewTestClientGetter()

			newBuilder := NewBuilder(tcg, testStartTime).
				WithInput(tc.input).
				WithOutput(tc.output).
				WithError(tc.error)
			gotEntrypointCommand := newSlurmBuilder(newBuilder).buildEntrypointCommand()
			if diff := cmp.Diff(tc.wantEntrypointCommand, gotEntrypointCommand); diff != "" {
				t.Errorf("Unexpected entrypoint command (-want/+got)\n%s", diff)
				return
			}
		})
	}
}
