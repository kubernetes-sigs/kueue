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

func TestBuildEntrypointScript(t *testing.T) {
	testCases := map[string]struct {
		slurmBuilder slurmBuilder
		wantScript   string
	}{
		"should build entrypoint script": {
			slurmBuilder: slurmBuilder{
				Builder: &Builder{
					nodes:  ptr.To[int32](2),
					nTasks: ptr.To[int32](2),
				},
				arrayIndexes: arrayIndexes{
					Indexes: []int32{0, 3, 6, 9, 12, 15, 18, 21, 24},
				},
			},
			wantScript: `#!/usr/bin/bash

set -o errexit
set -o nounset
set -o pipefail

# External
# JOB_COMPLETION_INDEX  - completion index of the job.
# JOB_CONTAINER_INDEX   - container index in the container template.

# COMPLETION_INDEX=CONTAINER_INDEX1,CONTAINER_INDEX2
declare -A array_indexes=(["0"]="0,3" ["1"]="6,9" ["2"]="12,15" ["3"]="18,21" ["4"]="24") 	# Requires bash 4+

container_indexes=${array_indexes[${JOB_COMPLETION_INDEX}]}
container_indexes=(${container_indexes//,/ })

if [[ ! -v container_indexes[${JOB_CONTAINER_INDEX}] ]];
then
	exit 0
fi

# Generated on the builder
export SLURM_ARRAY_JOB_ID=1       			# Job array’s master job ID number.
export SLURM_ARRAY_TASK_COUNT=9  		# Total number of tasks in a job array.
export SLURM_ARRAY_TASK_MAX=24    		# Job array’s maximum ID (index) number.
export SLURM_ARRAY_TASK_MIN=0    		# Job array’s minimum ID (index) number.
export SLURM_TASKS_PER_NODE=2    		# Job array’s master job ID number.
export SLURM_CPUS_PER_TASK=       			# Number of CPUs per task.
export SLURM_CPUS_ON_NODE=        			# Number of CPUs on the allocated node (actually pod).
export SLURM_JOB_CPUS_PER_NODE=   			# Count of processors available to the job on this node.
export SLURM_CPUS_PER_GPU=        			# Number of CPUs requested per allocated GPU.
export SLURM_MEM_PER_CPU=         			# Memory per CPU. Same as --mem-per-cpu .
export SLURM_MEM_PER_GPU=         			# Memory per GPU.
export SLURM_MEM_PER_NODE=        			# Memory per node. Same as --mem.
export SLURM_GPUS=                			# Number of GPUs requested (in total).
export SLURM_NTASKS=2              		# Same as -n, –ntasks. The number of tasks.
export SLURM_NTASKS_PER_NODE=$SLURM_NTASKS  # Number of tasks requested per node.
export SLURM_NPROCS=$SLURM_NTASKS       	# Same as -n, --ntasks. See $SLURM_NTASKS.
export SLURM_NNODES=2            		# Total number of nodes (actually pods) in the job’s resource allocation.
# export SLURM_SUBMIT_DIR=        			# The path of the job submission directory.
# export SLURM_SUBMIT_HOST=       			# The hostname of the node used for job submission.

# To be supported later
# export SLURM_JOB_NODELIST=        # Contains the definition (list) of the nodes (actually pods) that is assigned to the job. To be supported later.
# export SLURM_NODELIST=            # Deprecated. Same as SLURM_JOB_NODELIST. To be supported later.
# export SLURM_NTASKS_PER_SOCKET    # Number of tasks requested per socket. To be supported later.
# export SLURM_NTASKS_PER_CORE      # Number of tasks requested per core. To be supported later.
# export SLURM_NTASKS_PER_GPU       # Number of tasks requested per GPU. To be supported later.

# Calculated variables in runtime
export SLURM_JOB_ID=$(( JOB_COMPLETION_INDEX * SLURM_TASKS_PER_NODE + JOB_CONTAINER_INDEX + SLURM_ARRAY_JOB_ID ))   # The Job ID.
export SLURM_JOBID=$SLURM_JOB_ID                                                                                    # Deprecated. Same as $SLURM_JOB_ID
export SLURM_ARRAY_TASK_ID=${container_indexes[${JOB_CONTAINER_INDEX}]}												# Task ID.

bash /slurm/script.sh
`,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotScript := tc.slurmBuilder.buildEntrypointScript()
			if diff := cmp.Diff(tc.wantScript, gotScript); diff != "" {
				t.Errorf("Objects after build (-want,+got):\n%s", diff)
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

func TestSlurmBuilder(t *testing.T) {
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
					Completions(1).
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

# External
# JOB_COMPLETION_INDEX  - completion index of the job.
# JOB_CONTAINER_INDEX   - container index in the container template.

# COMPLETION_INDEX=CONTAINER_INDEX1,CONTAINER_INDEX2
declare -A array_indexes=(["0"]="0") 	# Requires bash 4+

container_indexes=${array_indexes[${JOB_COMPLETION_INDEX}]}
container_indexes=(${container_indexes//,/ })

if [[ ! -v container_indexes[${JOB_CONTAINER_INDEX}] ]];
then
	exit 0
fi

# Generated on the builder
export SLURM_ARRAY_JOB_ID=1       			# Job array’s master job ID number.
export SLURM_ARRAY_TASK_COUNT=1  		# Total number of tasks in a job array.
export SLURM_ARRAY_TASK_MAX=0    		# Job array’s maximum ID (index) number.
export SLURM_ARRAY_TASK_MIN=0    		# Job array’s minimum ID (index) number.
export SLURM_TASKS_PER_NODE=1    		# Job array’s master job ID number.
export SLURM_CPUS_PER_TASK=       			# Number of CPUs per task.
export SLURM_CPUS_ON_NODE=        			# Number of CPUs on the allocated node (actually pod).
export SLURM_JOB_CPUS_PER_NODE=   			# Count of processors available to the job on this node.
export SLURM_CPUS_PER_GPU=        			# Number of CPUs requested per allocated GPU.
export SLURM_MEM_PER_CPU=         			# Memory per CPU. Same as --mem-per-cpu .
export SLURM_MEM_PER_GPU=         			# Memory per GPU.
export SLURM_MEM_PER_NODE=        			# Memory per node. Same as --mem.
export SLURM_GPUS=                			# Number of GPUs requested (in total).
export SLURM_NTASKS=1              		# Same as -n, –ntasks. The number of tasks.
export SLURM_NTASKS_PER_NODE=$SLURM_NTASKS  # Number of tasks requested per node.
export SLURM_NPROCS=$SLURM_NTASKS       	# Same as -n, --ntasks. See $SLURM_NTASKS.
export SLURM_NNODES=1            		# Total number of nodes (actually pods) in the job’s resource allocation.
# export SLURM_SUBMIT_DIR=        			# The path of the job submission directory.
# export SLURM_SUBMIT_HOST=       			# The hostname of the node used for job submission.

# To be supported later
# export SLURM_JOB_NODELIST=        # Contains the definition (list) of the nodes (actually pods) that is assigned to the job. To be supported later.
# export SLURM_NODELIST=            # Deprecated. Same as SLURM_JOB_NODELIST. To be supported later.
# export SLURM_NTASKS_PER_SOCKET    # Number of tasks requested per socket. To be supported later.
# export SLURM_NTASKS_PER_CORE      # Number of tasks requested per core. To be supported later.
# export SLURM_NTASKS_PER_GPU       # Number of tasks requested per GPU. To be supported later.

# Calculated variables in runtime
export SLURM_JOB_ID=$(( JOB_COMPLETION_INDEX * SLURM_TASKS_PER_NODE + JOB_CONTAINER_INDEX + SLURM_ARRAY_JOB_ID ))   # The Job ID.
export SLURM_JOBID=$SLURM_JOB_ID                                                                                    # Deprecated. Same as $SLURM_JOB_ID
export SLURM_ARRAY_TASK_ID=${container_indexes[${JOB_CONTAINER_INDEX}]}												# Task ID.

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
				WithStdOut(tc.output).
				WithStdErr(tc.err).
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
