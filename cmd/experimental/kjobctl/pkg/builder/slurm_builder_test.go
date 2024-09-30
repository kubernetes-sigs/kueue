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
	"k8s.io/utils/ptr"

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
	beforeTest    func(tc *slurmBuilderTestCase) error
	afterTest     func(tc *slurmBuilderTestCase) error
	tempFile      string
	namespace     string
	profile       string
	mode          v1alpha1.ApplicationProfileMode
	array         string
	cpusPerTask   *resource.Quantity
	gpusPerTask   map[string]*resource.Quantity
	memPerTask    *resource.Quantity
	memPerCPU     *resource.Quantity
	memPerGPU     *resource.Quantity
	nodes         *int32
	nTasks        *int32
	output        string
	err           string
	input         string
	jobName       string
	partition     string
	kjobctlObjs   []runtime.Object
	wantRootObj   runtime.Object
	wantChildObjs []runtime.Object
	wantErr       error
	cmpopts       []cmp.Option
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
		"shouldn't build slurm job because script not specified": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.SlurmMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{Name: v1alpha1.SlurmMode, Template: "slurm-template"}).
					Obj(),
			},
			wantErr: noScriptSpecifiedErr,
		},
		"shouldn't build slurm job because template not found": {
			beforeTest: beforeSlurmTest,
			afterTest:  afterSlurmTest,
			namespace:  metav1.NamespaceDefault,
			profile:    "profile",
			mode:       v1alpha1.SlurmMode,
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
			wantRootObj: wrappers.MakeJob("", metav1.NamespaceDefault).
				Parallelism(2).
				Completions(5).
				CompletionMode(batchv1.IndexedCompletion).
				Profile("profile").
				Mode(v1alpha1.SlurmMode).
				WithInitContainer(*wrappers.MakeContainer("slurm-init-env", "bash:5-alpine3.20").
					Command("bash", "/slurm/scripts/init-entrypoint.sh").
					WithVolumeMount(corev1.VolumeMount{Name: "slurm-scripts", MountPath: "/slurm/scripts"}).
					WithVolumeMount(corev1.VolumeMount{Name: "slurm-env", MountPath: "/slurm/env"}).
					Obj()).
				WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").
					Command("bash", "/slurm/scripts/entrypoint.sh").
					WithVolumeMount(corev1.VolumeMount{Name: "slurm-scripts", MountPath: "/slurm/scripts"}).
					WithVolumeMount(corev1.VolumeMount{Name: "slurm-env", MountPath: "/slurm/env"}).
					WithEnvVar(corev1.EnvVar{Name: constants.EnvVarNameUserID, Value: userID}).
					WithEnvVar(corev1.EnvVar{Name: constants.EnvVarTaskName, Value: "default_profile"}).
					WithEnvVar(corev1.EnvVar{
						Name:  constants.EnvVarTaskID,
						Value: fmt.Sprintf("%s_%s_default_profile", userID, testStartTime.Format(time.RFC3339)),
					}).
					WithEnvVar(corev1.EnvVar{Name: "PROFILE", Value: "default_profile"}).
					WithEnvVar(corev1.EnvVar{Name: "TIMESTAMP", Value: testStartTime.Format(time.RFC3339)}).
					WithEnvVar(corev1.EnvVar{Name: "JOB_CONTAINER_INDEX", Value: "0"}).
					Obj()).
				WithVolume(corev1.Volume{
					Name: "slurm-scripts",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							Items: []corev1.KeyToPath{
								{Key: "init-entrypoint.sh", Path: "init-entrypoint.sh"},
								{Key: "entrypoint.sh", Path: "entrypoint.sh"},
								{Key: "script", Path: "script", Mode: ptr.To[int32](0755)},
							},
						},
					},
				}).
				WithVolume(corev1.Volume{
					Name: "slurm-env",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				}).
				Obj(),
			wantChildObjs: []runtime.Object{
				wrappers.MakeConfigMap("", metav1.NamespaceDefault).
					Profile("profile").
					Mode(v1alpha1.SlurmMode).
					Data(map[string]string{
						"script": "#!/bin/bash\nsleep 300'",
						"init-entrypoint.sh": `#!/usr/local/bin/bash

set -o errexit
set -o nounset
set -o pipefail

# External variables
# JOB_COMPLETION_INDEX  - completion index of the job.

for i in {0..1}
do
  # ["COMPLETION_INDEX"]="CONTAINER_INDEX_1,CONTAINER_INDEX_2"
	declare -A array_indexes=(["0"]="1" ["1"]="2" ["2"]="3" ["3"]="4" ["4"]="5") 	# Requires bash v4+

	container_indexes=${array_indexes[${JOB_COMPLETION_INDEX}]}
	container_indexes=(${container_indexes//,/ })

	if [[ ! -v container_indexes[$i] ]];
	then
		break
	fi

	mkdir -p /slurm/env/$i

	cat << EOF > /slurm/env/$i/sbatch.env
SBATCH_ARRAY_INX=1-5%2
SBATCH_GPUS_PER_TASK=
SBATCH_MEM_PER_CPU=
SBATCH_MEM_PER_GPU=
SBATCH_OUTPUT=
SBATCH_ERROR=
SBATCH_INPUT=
SBATCH_JOB_NAME=
SBATCH_PARTITION=
EOF

	cat << EOF > /slurm/env/$i/slurm.env
SLURM_ARRAY_JOB_ID=1
SLURM_ARRAY_TASK_COUNT=5
SLURM_ARRAY_TASK_MAX=5
SLURM_ARRAY_TASK_MIN=1
SLURM_TASKS_PER_NODE=1
SLURM_CPUS_PER_TASK=
SLURM_CPUS_ON_NODE=
SLURM_JOB_CPUS_PER_NODE=
SLURM_CPUS_PER_GPU=
SLURM_MEM_PER_CPU=
SLURM_MEM_PER_GPU=
SLURM_MEM_PER_NODE=
SLURM_GPUS=0
SLURM_NTASKS=1
SLURM_NTASKS_PER_NODE=1
SLURM_NPROCS=1
SLURM_NNODES=2
SLURM_SUBMIT_DIR=/slurm/scripts
SLURM_SUBMIT_HOST=$HOSTNAME
SLURM_JOB_ID=$(( JOB_COMPLETION_INDEX * 1 + i + 1 ))
SLURM_JOBID=$(( JOB_COMPLETION_INDEX * 1 + i + 1 ))
SLURM_ARRAY_TASK_ID=${container_indexes[$i]}
EOF

done
`,
						"entrypoint.sh": `#!/usr/local/bin/bash

set -o errexit
set -o nounset
set -o pipefail

# External variables
# JOB_CONTAINER_INDEX 	- container index in the container template.

if [ ! -d "/slurm/env/$JOB_CONTAINER_INDEX" ]; then
	exit 0
fi

source /slurm/env/$JOB_CONTAINER_INDEX/sbatch.env

export $(cat /slurm/env/$JOB_CONTAINER_INDEX/slurm.env | xargs)

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

/slurm/scripts/script
`,
					}).
					Obj(),
			},
			cmpopts: []cmp.Option{
				cmpopts.IgnoreFields(corev1.LocalObjectReference{}, "Name"),
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

			gotRootObj, gotChildObjs, gotErr := NewBuilder(tcg, testStartTime).
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

			if diff := cmp.Diff(tc.wantRootObj, gotRootObj, opts...); diff != "" {
				t.Errorf("Root object after build (-want,+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantChildObjs, gotChildObjs, opts...); diff != "" {
				t.Errorf("Child objects after build (-want,+got):\n%s", diff)
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
			wantEntrypointCommand: "/slurm/scripts/script",
		},
		"should build entrypoint command with output": {
			output:                "/home/test/stdout.out",
			wantEntrypointCommand: "/slurm/scripts/script 1>$output_file",
		},
		"should build entrypoint command with error": {
			error:                 "/home/test/stderr.out",
			wantEntrypointCommand: "/slurm/scripts/script 2>$error_file",
		},
		"should build entrypoint command with output and error": {
			output:                "/home/test/stdout.out",
			error:                 "/home/test/stderr.out",
			wantEntrypointCommand: "/slurm/scripts/script 1>$output_file 2>$error_file",
		},
		"should build entrypoint command with input": {
			input:                 "/home/test/script.sh",
			wantEntrypointCommand: "/slurm/scripts/script <$input_file",
		},
		"should build entrypoint command with input and output": {
			input:                 "/home/test/script.sh",
			output:                "/home/test/stdout.out",
			wantEntrypointCommand: "/slurm/scripts/script <$input_file 1>$output_file",
		},
		"should build entrypoint command with input and error": {
			input:                 "/home/test/script.sh",
			error:                 "/home/test/stderr.out",
			wantEntrypointCommand: "/slurm/scripts/script <$input_file 2>$error_file",
		},
		"should build entrypoint command with input, output and error": {
			input:                 "/home/test/script.sh",
			output:                "/home/test/stdout.out",
			error:                 "/home/test/stderr.out",
			wantEntrypointCommand: "/slurm/scripts/script <$input_file 1>$output_file 2>$error_file",
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
