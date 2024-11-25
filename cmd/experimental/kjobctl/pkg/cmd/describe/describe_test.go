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

package describe

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/resource"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	restfake "k8s.io/client-go/rest/fake"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	cmdtesting "sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/testing"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/testing/wrappers"
)

func TestDescribeCmd(t *testing.T) {
	testCases := map[string]struct {
		args             []string
		argsFormat       int
		objs             []runtime.Object
		withK8sClientSet bool
		mapperKinds      []schema.GroupVersionKind
		wantOut          string
		wantOutErr       string
		wantErr          error
	}{
		"shouldn't describe none kjobctl owned jobs": {
			args:       []string{"job", "sample-job-8c7zt"},
			argsFormat: modeTaskArgsFormat,
			mapperKinds: []schema.GroupVersionKind{
				batchv1.SchemeGroupVersion.WithKind("Job"),
			},
			objs: []runtime.Object{
				wrappers.MakeJob("sample-job-8c7zt", metav1.NamespaceDefault).Obj(),
			},
			wantOutErr: "No resources found in default namespace.\n",
		},
		"describe job with 'mode task' format": {
			args:       []string{"job", "sample-job-8c7zt"},
			argsFormat: modeTaskArgsFormat,
			mapperKinds: []schema.GroupVersionKind{
				batchv1.SchemeGroupVersion.WithKind("Job"),
			},
			objs: []runtime.Object{
				getSampleJob("sample-job-8c7zt"),
			},
			wantOut: `Name:           sample-job-8c7zt
Namespace:      default
Labels:         kjobctl.x-k8s.io/profile=sample-profile
Annotations:    kjobctl.x-k8s.io/script=test.sh
Parallelism:    3
Completions:    2
Start Time:     Mon, 01 Jan 2024 00:00:00 +0000
Completed At:   Mon, 01 Jan 2024 00:00:33 +0000
Duration:       33s
Pods Statuses:  0 Active / 2 Succeeded / 0 Failed
Pod Template:
  Containers:
   sample-container:
    Port:       <none>
    Host Port:  <none>
    Command:
      sleep
      15s
    Args:
      30s
    Requests:
      cpu:        1
      memory:     200Mi
    Environment:  <none>
    Mounts:       <none>
  Volumes:        <none>
`,
		},
		"describe slurm with 'mode task' format": {
			args:       []string{"slurm", "sample-job-8c7zt"},
			argsFormat: modeTaskArgsFormat,
			mapperKinds: []schema.GroupVersionKind{
				batchv1.SchemeGroupVersion.WithKind("Job"),
				corev1.SchemeGroupVersion.WithKind("ConfigMap"),
			},
			withK8sClientSet: true,
			objs: []runtime.Object{
				getSampleJob("sample-job-8c7zt"),
				wrappers.MakeConfigMap("sample-job-8c7zt", "default").
					Profile("profile").
					Mode(v1alpha1.SlurmMode).
					Data(map[string]string{
						"script": "#!/bin/bash\nsleep 300",
						"entrypoint.sh": `#!/usr/bin/bash

set -o errexit
set -o nounset
set -o pipefail

# External variables
# JOB_COMPLETION_INDEX  - completion index of the job.
# JOB_CONTAINER_INDEX   - container index in the container template.

# ["COMPLETION_INDEX"]="CONTAINER_INDEX_1,CONTAINER_INDEX_2"
declare -A array_indexes=(["0"]="0") 	# Requires bash v4+

container_indexes=${array_indexes[${JOB_COMPLETION_INDEX}]}
container_indexes=(${container_indexes//,/ })

if [[ ! -v container_indexes[${JOB_CONTAINER_INDEX}] ]];
then
exit 0
fi

SBATCH_ARRAY_INX=
SBATCH_GPUS_PER_TASK=
SBATCH_MEM_PER_CPU=
SBATCH_MEM_PER_GPU=
SBATCH_OUTPUT=
SBATCH_ERROR=
SBATCH_INPUT=
SBATCH_JOB_NAME=
SBATCH_PARTITION=

export SLURM_ARRAY_JOB_ID=1       		# Job array’s master job ID number.
export SLURM_ARRAY_TASK_COUNT=1  		# Total number of tasks in a job array.
export SLURM_ARRAY_TASK_MAX=0    		# Job array’s maximum ID (index) number.
export SLURM_ARRAY_TASK_MIN=0    		# Job array’s minimum ID (index) number.
export SLURM_TASKS_PER_NODE=1    		# Number of tasks to be initiated on each node.
export SLURM_CPUS_PER_TASK=       		# Number of CPUs per task.
export SLURM_CPUS_ON_NODE=        		# Number of CPUs on the allocated node (actually pod).
export SLURM_JOB_CPUS_PER_NODE=   		# Count of processors available to the job on this node.
export SLURM_CPUS_PER_GPU=        		# Number of CPUs requested per allocated GPU.
export SLURM_MEM_PER_CPU=         	# Memory per CPU. Same as --mem-per-cpu .
export SLURM_MEM_PER_GPU=         	# Memory per GPU.
export SLURM_MEM_PER_NODE=        	# Memory per node. Same as --mem.
export SLURM_GPUS=0                	# Number of GPUs requested (in total).
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

/slurm/script
`,
					}).
					Obj(),
			},
			wantOut: `Name:           sample-job-8c7zt
Namespace:      default
Labels:         kjobctl.x-k8s.io/profile=sample-profile
Annotations:    kjobctl.x-k8s.io/script=test.sh
Parallelism:    3
Completions:    2
Start Time:     Mon, 01 Jan 2024 00:00:00 +0000
Completed At:   Mon, 01 Jan 2024 00:00:33 +0000
Duration:       33s
Pods Statuses:  0 Active / 2 Succeeded / 0 Failed
Pod Template:
  Containers:
   sample-container:
    Port:       <none>
    Host Port:  <none>
    Command:
      sleep
      15s
    Args:
      30s
    Requests:
      cpu:        1
      memory:     200Mi
    Environment:  <none>
    Mounts:       <none>
  Volumes:        <none>


Name:       sample-job-8c7zt
Namespace:  default
Labels:     kjobctl.x-k8s.io/mode=Slurm
            kjobctl.x-k8s.io/profile=profile

Data
====
entrypoint.sh:
----
#!/usr/bin/bash

set -o errexit
set -o nounset
set -o pipefail

# External variables
# JOB_COMPLETION_INDEX  - completion index of the job.
# JOB_CONTAINER_INDEX   - container index in the container template.

# ["COMPLETION_INDEX"]="CONTAINER_INDEX_1,CONTAINER_INDEX_2"
declare -A array_indexes=(["0"]="0")   # Requires bash v4+

container_indexes=${array_indexes[${JOB_COMPLETION_INDEX}]}
container_indexes=(${container_indexes//,/ })

if [[ ! -v container_indexes[${JOB_CONTAINER_INDEX}] ]];
then
exit 0
fi

SBATCH_ARRAY_INX=
SBATCH_GPUS_PER_TASK=
SBATCH_MEM_PER_CPU=
SBATCH_MEM_PER_GPU=
SBATCH_OUTPUT=
SBATCH_ERROR=
SBATCH_INPUT=
SBATCH_JOB_NAME=
SBATCH_PARTITION=

export SLURM_ARRAY_JOB_ID=1                  # Job array’s master job ID number.
export SLURM_ARRAY_TASK_COUNT=1              # Total number of tasks in a job array.
export SLURM_ARRAY_TASK_MAX=0                # Job array’s maximum ID (index) number.
export SLURM_ARRAY_TASK_MIN=0                # Job array’s minimum ID (index) number.
export SLURM_TASKS_PER_NODE=1                # Number of tasks to be initiated on each node.
export SLURM_CPUS_PER_TASK=                  # Number of CPUs per task.
export SLURM_CPUS_ON_NODE=                   # Number of CPUs on the allocated node (actually pod).
export SLURM_JOB_CPUS_PER_NODE=              # Count of processors available to the job on this node.
export SLURM_CPUS_PER_GPU=                   # Number of CPUs requested per allocated GPU.
export SLURM_MEM_PER_CPU=                  # Memory per CPU. Same as --mem-per-cpu .
export SLURM_MEM_PER_GPU=                  # Memory per GPU.
export SLURM_MEM_PER_NODE=                 # Memory per node. Same as --mem.
export SLURM_GPUS=0                        # Number of GPUs requested (in total).
export SLURM_NTASKS=1                      # Same as -n, –ntasks. The number of tasks.
export SLURM_NTASKS_PER_NODE=1               # Number of tasks requested per node.
export SLURM_NPROCS=$SLURM_NTASKS          # Same as -n, --ntasks. See $SLURM_NTASKS.
export SLURM_NNODES=1                        # Total number of nodes (actually pods) in the job’s resource allocation.
export SLURM_SUBMIT_DIR=/slurm               # The path of the job submission directory.
export SLURM_SUBMIT_HOST=$HOSTNAME         # The hostname of the node used for job submission.

export SLURM_JOB_ID=$(( JOB_COMPLETION_INDEX * SLURM_TASKS_PER_NODE + JOB_CONTAINER_INDEX + SLURM_ARRAY_JOB_ID ))   # The Job ID.
export SLURM_JOBID=$SLURM_JOB_ID                                                                                    # Deprecated. Same as $SLURM_JOB_ID
export SLURM_ARRAY_TASK_ID=${container_indexes[${JOB_CONTAINER_INDEX}]}                        # Task ID.

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

/slurm/script


script:
----
#!/bin/bash
sleep 300


BinaryData
====

`,
		},
		"describe specific task with 'mode slash task' format": {
			args:       []string{"job/sample-job-8c7zt"},
			argsFormat: modeTaskArgsFormat,
			mapperKinds: []schema.GroupVersionKind{
				batchv1.SchemeGroupVersion.WithKind("Job"),
			},
			objs: []runtime.Object{
				getSampleJob("sample-job-8c7zt"),
			},
			wantOut: `Name:           sample-job-8c7zt
Namespace:      default
Labels:         kjobctl.x-k8s.io/profile=sample-profile
Annotations:    kjobctl.x-k8s.io/script=test.sh
Parallelism:    3
Completions:    2
Start Time:     Mon, 01 Jan 2024 00:00:00 +0000
Completed At:   Mon, 01 Jan 2024 00:00:33 +0000
Duration:       33s
Pods Statuses:  0 Active / 2 Succeeded / 0 Failed
Pod Template:
  Containers:
   sample-container:
    Port:       <none>
    Host Port:  <none>
    Command:
      sleep
      15s
    Args:
      30s
    Requests:
      cpu:        1
      memory:     200Mi
    Environment:  <none>
    Mounts:       <none>
  Volumes:        <none>
`,
		},
		"describe all jobs": {
			args:       []string{"job"},
			argsFormat: modeTaskArgsFormat,
			mapperKinds: []schema.GroupVersionKind{
				batchv1.SchemeGroupVersion.WithKind("Job"),
			},
			objs: []runtime.Object{
				&batchv1.JobList{
					Items: []batchv1.Job{
						*getSampleJob("sample-job-5zd6r"),
						*getSampleJob("sample-job-8c7zt"),
					},
				},
			},
			wantOut: `Name:           sample-job-5zd6r
Namespace:      default
Labels:         kjobctl.x-k8s.io/profile=sample-profile
Annotations:    kjobctl.x-k8s.io/script=test.sh
Parallelism:    3
Completions:    2
Start Time:     Mon, 01 Jan 2024 00:00:00 +0000
Completed At:   Mon, 01 Jan 2024 00:00:33 +0000
Duration:       33s
Pods Statuses:  0 Active / 2 Succeeded / 0 Failed
Pod Template:
  Containers:
   sample-container:
    Port:       <none>
    Host Port:  <none>
    Command:
      sleep
      15s
    Args:
      30s
    Requests:
      cpu:        1
      memory:     200Mi
    Environment:  <none>
    Mounts:       <none>
  Volumes:        <none>


Name:           sample-job-8c7zt
Namespace:      default
Labels:         kjobctl.x-k8s.io/profile=sample-profile
Annotations:    kjobctl.x-k8s.io/script=test.sh
Parallelism:    3
Completions:    2
Start Time:     Mon, 01 Jan 2024 00:00:00 +0000
Completed At:   Mon, 01 Jan 2024 00:00:33 +0000
Duration:       33s
Pods Statuses:  0 Active / 2 Succeeded / 0 Failed
Pod Template:
  Containers:
   sample-container:
    Port:       <none>
    Host Port:  <none>
    Command:
      sleep
      15s
    Args:
      30s
    Requests:
      cpu:        1
      memory:     200Mi
    Environment:  <none>
    Mounts:       <none>
  Volumes:        <none>
`,
		},
		"describe interactive with 'mode task' format": {
			args:       []string{"interactive", "sample-interactive-fgnh9"},
			argsFormat: modeTaskArgsFormat,
			mapperKinds: []schema.GroupVersionKind{
				corev1.SchemeGroupVersion.WithKind("Pod"),
			},
			objs: []runtime.Object{
				getSampleInteractive("sample-interactive-fgnh9"),
			},
			wantOut: `Name:        sample-interactive-fgnh9
Namespace:   default
Start Time:  Mon, 01 Jan 2024 00:00:00 +0000
Labels:      kjobctl.x-k8s.io/profile=sample-profile
Status:      Running
Containers:
  sample-container:
    Port:       <none>
    Host Port:  <none>
    Command:
      /bin/sh
    Environment:
      TASK_NAME:  sample-interactive
    Mounts:
      /sample from sample-volume (rw)
Volumes:
  sample-volume:
    Type:       EmptyDir (a temporary directory that shares a pod's lifetime)
    Medium:     
    SizeLimit:  <unset>
`,
		},
		"describe all interactive tasks": {
			args:       []string{"interactive"},
			argsFormat: modeTaskArgsFormat,
			mapperKinds: []schema.GroupVersionKind{
				corev1.SchemeGroupVersion.WithKind("Pod"),
			},
			objs: []runtime.Object{
				&corev1.PodList{
					Items: []corev1.Pod{
						*getSampleInteractive("sample-interactive-fgnh9"),
						*getSampleInteractive("sample-interactive-hs2b2"),
					},
				},
			},
			wantOut: `Name:        sample-interactive-fgnh9
Namespace:   default
Start Time:  Mon, 01 Jan 2024 00:00:00 +0000
Labels:      kjobctl.x-k8s.io/profile=sample-profile
Status:      Running
Containers:
  sample-container:
    Port:       <none>
    Host Port:  <none>
    Command:
      /bin/sh
    Environment:
      TASK_NAME:  sample-interactive
    Mounts:
      /sample from sample-volume (rw)
Volumes:
  sample-volume:
    Type:       EmptyDir (a temporary directory that shares a pod's lifetime)
    Medium:     
    SizeLimit:  <unset>


Name:        sample-interactive-hs2b2
Namespace:   default
Start Time:  Mon, 01 Jan 2024 00:00:00 +0000
Labels:      kjobctl.x-k8s.io/profile=sample-profile
Status:      Running
Containers:
  sample-container:
    Port:       <none>
    Host Port:  <none>
    Command:
      /bin/sh
    Environment:
      TASK_NAME:  sample-interactive
    Mounts:
      /sample from sample-volume (rw)
Volumes:
  sample-volume:
    Type:       EmptyDir (a temporary directory that shares a pod's lifetime)
    Medium:     
    SizeLimit:  <unset>
`,
		},
		"describe ray job with 'mode task' format": {
			args:       []string{"rayjob", "sample-ray-job"},
			argsFormat: modeTaskArgsFormat,
			mapperKinds: []schema.GroupVersionKind{
				rayv1.SchemeGroupVersion.WithKind("RayJob"),
			},
			objs: []runtime.Object{
				wrappers.MakeRayJob("sample-ray-job", metav1.NamespaceDefault).
					Profile("my-profile").
					LocalQueue("lq").
					StartTime(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)).
					EndTime(time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC)).
					JobDeploymentStatus(rayv1.JobDeploymentStatusRunning).
					JobStatus(rayv1.JobStatusFailed).
					Reason(rayv1.DeadlineExceeded).
					Message("Error message").
					RayClusterName("my-cluster").
					Obj(),
			},
			wantOut: `Name:                   sample-ray-job
Namespace:              default
Start Time:             Mon, 01 Jan 2024 00:00:00 +0000
End Time:               Mon, 01 Jan 2024 01:00:00 +0000
Labels:                 kjobctl.x-k8s.io/profile=my-profile
                        kueue.x-k8s.io/queue-name=lq
Job Deployment Status:  Running
Job Status:             FAILED
Reason:                 DeadlineExceeded
Message:                Error message
Ray Cluster Name:       my-cluster
Ray Cluster Status:
  Desired CPU:     0
  Desired GPU:     0
  Desired Memory:  0
  Desired TPU:     0
`,
		},
		"describe ray cluster with 'mode task' format": {
			args:       []string{"raycluster", "sample-ray-cluster"},
			argsFormat: modeTaskArgsFormat,
			mapperKinds: []schema.GroupVersionKind{
				rayv1.SchemeGroupVersion.WithKind("RayCluster"),
			},
			objs: []runtime.Object{
				wrappers.MakeRayCluster("sample-ray-job", metav1.NamespaceDefault).
					Profile("my-profile").
					LocalQueue("lq").
					State(rayv1.Failed).
					Reason("Reason message").
					DesiredCPU(apiresource.MustParse("1")).
					DesiredGPU(apiresource.MustParse("5")).
					DesiredMemory(apiresource.MustParse("2Gi")).
					DesiredTPU(apiresource.MustParse("10")).
					ReadyWorkerReplicas(1).
					AvailableWorkerReplicas(1).
					DesiredWorkerReplicas(1).
					MinWorkerReplicas(1).
					MaxWorkerReplicas(5).
					Spec(
						*wrappers.MakeRayClusterSpec().
							HeadGroupSpec(rayv1.HeadGroupSpec{
								RayStartParams: map[string]string{"p1": "v1", "p2": "v2"},
								Template: corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{
										Containers: []corev1.Container{
											*wrappers.MakeContainer("ray-head", "rayproject/ray:2.9.0").
												WithEnvVar(corev1.EnvVar{Name: "TASK_NAME", Value: "sample-interactive"}).
												WithVolumeMount(corev1.VolumeMount{Name: "sample-volume", MountPath: "/sample"}).
												Obj(),
										},
									},
								},
							}).
							WithWorkerGroupSpec(
								*wrappers.MakeWorkerGroupSpec("group1").
									Replicas(1).
									MinReplicas(1).
									MaxReplicas(5).
									RayStartParams(map[string]string{"p1": "v1", "p2": "v2"}).
									WithContainer(
										*wrappers.MakeContainer("ray-worker", "rayproject/ray:2.9.0").
											WithEnvVar(corev1.EnvVar{Name: "TASK_NAME", Value: "sample-interactive"}).
											WithVolumeMount(corev1.VolumeMount{Name: "sample-volume", MountPath: "/sample"}).
											Obj(),
									).
									Obj(),
							).
							Suspend(true).
							Obj(),
					).
					Obj(),
			},
			wantOut: `Name:                       sample-ray-job
Namespace:                  default
Labels:                     kjobctl.x-k8s.io/profile=my-profile
                            kueue.x-k8s.io/queue-name=lq
Suspend:                    true
State:                      failed
Reason:                     Reason message
Desired CPU:                1
Desired GPU:                5
Desired Memory:             2Gi
Desired TPU:                10
Ready Worker Replicas:      1
Available Worker Replicas:  1
Desired Worker Replicas:    1
Min Worker Replicas:        1
Max Worker Replicas:        5
Head Group:
  Start Params:    p1=v1
                   p2=v2
  Pod Template:
    Containers:
     ray-head:
      Port:       <none>
      Host Port:  <none>
      Environment:
        TASK_NAME:  sample-interactive
      Mounts:
        /sample from sample-volume (rw)
    Volumes:  <none>
Worker Groups:
  group1:
    Replicas:      1
    Min Replicas:  1
    Max Replicas:  5
    Start Params:      p1=v1
                       p2=v2
    Pod Template:
      Containers:
       ray-worker:
        Port:       <none>
        Host Port:  <none>
        Environment:
          TASK_NAME:  sample-interactive
        Mounts:
          /sample from sample-volume (rw)
      Volumes:  <none>
`,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("TZ", "")
			streams, _, out, outErr := genericiooptions.NewTestIOStreams()

			tcg := cmdtesting.NewTestClientGetter()
			if tc.withK8sClientSet {
				tcg.WithK8sClientset(k8sfake.NewSimpleClientset(tc.objs...))
			}

			if len(tc.mapperKinds) != 0 {
				mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{})
				for _, k := range tc.mapperKinds {
					mapper.Add(k, meta.RESTScopeNamespace)
				}
				tcg.WithRESTMapper(mapper)
			}

			if len(tc.objs) != 0 {
				scheme := runtime.NewScheme()

				if err := batchv1.AddToScheme(scheme); err != nil {
					t.Errorf("Unexpected error\n%s", err)
				}

				if err := corev1.AddToScheme(scheme); err != nil {
					t.Errorf("Unexpected error\n%s", err)
				}

				if err := rayv1.AddToScheme(scheme); err != nil {
					t.Errorf("Unexpected error\n%s", err)
				}

				codec := serializer.NewCodecFactory(scheme).LegacyCodec(scheme.PrioritizedVersionsAllGroups()...)
				tcg.WithRESTClient(&restfake.RESTClient{
					NegotiatedSerializer: resource.UnstructuredPlusDefaultContentConfig().NegotiatedSerializer,
					Resp: &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(runtime.EncodeOrDie(codec, tc.objs[0]))),
					},
				})
			}

			cmd := NewDescribeCmd(tcg, streams)
			cmd.SetArgs(tc.args)

			gotErr := cmd.Execute()
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}

			gotOut := out.String()
			if diff := cmp.Diff(tc.wantOut, gotOut); diff != "" {
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}

			gotOutErr := outErr.String()
			if diff := cmp.Diff(tc.wantOutErr, gotOutErr); diff != "" {
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}
		})
	}
}

func getSampleJob(name string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				"kjobctl.x-k8s.io/profile": "sample-profile",
			},
			Annotations: map[string]string{
				constants.ScriptAnnotation: "test.sh",
			},
		},
		Spec: batchv1.JobSpec{
			Parallelism: ptr.To[int32](3),
			Completions: ptr.To[int32](2),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "sample-container",
							Command: []string{"sleep", "15s"},
							Args:    []string{"30s"},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    apiresource.MustParse("1"),
									corev1.ResourceMemory: apiresource.MustParse("200Mi"),
								},
							},
						},
					},
				},
			},
		},
		Status: batchv1.JobStatus{
			Active:         0,
			Succeeded:      2,
			Failed:         0,
			StartTime:      ptr.To(metav1.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)),
			CompletionTime: ptr.To(metav1.Date(2024, 1, 1, 0, 0, 33, 0, time.UTC)),
		},
	}
}

func getSampleInteractive(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				"kjobctl.x-k8s.io/profile": "sample-profile",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "sample-container",
					Command: []string{"/bin/sh"},
					Env: []corev1.EnvVar{
						{
							Name:  "TASK_NAME",
							Value: "sample-interactive",
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "sample-volume",
							MountPath: "/sample",
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "sample-volume",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			StartTime: ptr.To(metav1.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)),
		},
	}
}
