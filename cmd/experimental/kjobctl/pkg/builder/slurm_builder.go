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
	"bufio"
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/parser"
)

const (
	slurmScriptFilename     = "script.sh"
	slurmEntrypointFilename = "entrypoint.sh"
	slurmPath               = "/slurm"
)

type slurmBuilder struct {
	*Builder

	arrayIndexes parser.ArrayIndexes
}

var _ builder = (*slurmBuilder)(nil)

func (b *slurmBuilder) build(ctx context.Context) ([]runtime.Object, error) {
	template, err := b.kjobctlClientset.KjobctlV1alpha1().JobTemplates(b.profile.Namespace).
		Get(ctx, string(b.mode.Template), metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	job := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Job",
			APIVersion: "batch/v1",
		},
		ObjectMeta: b.buildObjectMeta(template.ObjectMeta),
		Spec:       template.Template.Spec,
	}
	job.Spec.CompletionMode = ptr.To(batchv1.IndexedCompletion)

	content, err := os.ReadFile(b.script)
	if err != nil {
		return nil, err
	}
	script := bufio.NewScanner(bytes.NewReader(content))
	scriptFlags, err := parser.SlurmFlags(script, b.ignoreUnknown)
	if err != nil {
		return nil, err
	}
	b.replaceCLIFlags(scriptFlags)

	var objectName string
	if b.jobName == "" {
		objectName = b.generatePrefixName() + utilrand.String(5)
	} else {
		objectName = b.jobName
	}
	job.ObjectMeta.GenerateName = ""
	job.ObjectMeta.Name = objectName

	if b.array == "" {
		b.arrayIndexes = parser.GenerateArrayIndexes(ptr.Deref(b.nodes, 1))
	} else {
		b.arrayIndexes, err = parser.ParseArrayIndexes(b.array)
		if err != nil {
			return nil, err
		}
	}

	configMap := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: b.buildObjectMeta(template.ObjectMeta),
		Data: map[string]string{
			slurmEntrypointFilename: b.buildEntrypointScript(),
			slurmScriptFilename:     string(content),
		},
	}
	configMap.ObjectMeta.GenerateName = ""
	configMap.ObjectMeta.Name = objectName

	b.buildPodSpecVolumesAndEnv(&job.Spec.Template.Spec)
	job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
		Name: configMap.Name,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: configMap.Name,
				},
				Items: []corev1.KeyToPath{
					{
						Key:  slurmEntrypointFilename,
						Path: slurmEntrypointFilename,
					},
					{
						Key:  slurmScriptFilename,
						Path: slurmScriptFilename,
					},
				},
			},
		},
	})

	for i := range job.Spec.Template.Spec.Containers {
		container := &job.Spec.Template.Spec.Containers[i]

		container.Command = []string{"bash", fmt.Sprintf("%s/entrypoint.sh", slurmPath)}

		if len(b.requests) > 0 {
			container.Resources.Requests = b.requests
		}

		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      configMap.Name,
			MountPath: slurmPath,
		})
	}

	var completions int32
	nTasks := ptr.Deref(b.nTasks, 1)
	if len(b.arrayIndexes.Indexes) > 0 {
		totalIndexes := float64(len(b.arrayIndexes.Indexes))
		completions = int32(math.Ceil(totalIndexes / float64(nTasks)))
		job.Spec.Completions = ptr.To(completions)

		job.Spec.Parallelism = b.arrayIndexes.Parallelism
	}

	var updatedContainers []corev1.Container
	for jobContainerIndex := range nTasks {
		containers := make([]corev1.Container, len(job.Spec.Template.Spec.Containers))
		copy(containers, job.Spec.Template.Spec.Containers)

		for i := range containers {
			containers[i].Name = fmt.Sprintf("%s-%d", containers[i].Name, jobContainerIndex)
			containers[i].Env = append(containers[i].Env, corev1.EnvVar{
				Name:  "JOB_CONTAINER_INDEX",
				Value: strconv.FormatInt(int64(jobContainerIndex), 10),
			})
		}
		updatedContainers = append(updatedContainers, containers...)
	}
	job.Spec.Template.Spec.Containers = updatedContainers

	if b.nodes != nil {
		job.Spec.Parallelism = b.nodes
	}

	return []runtime.Object{job, configMap}, nil
}

func (b *slurmBuilder) buildIndexesMap() map[int32][]int32 {
	indexMap := make(map[int32][]int32)
	nTasks := ptr.Deref(b.nTasks, 1)
	var (
		completionIndex int32
		containerIndex  int32
	)
	for _, index := range b.arrayIndexes.Indexes {
		indexMap[completionIndex] = append(indexMap[completionIndex], index)
		containerIndex++
		if containerIndex >= nTasks {
			containerIndex = 0
			completionIndex++
		}
	}
	return indexMap
}

func (b *slurmBuilder) buildEntrypointScript() string {
	indexesMap := b.buildIndexesMap()
	keyValues := make([]string, 0, len(indexesMap))
	for key, value := range indexesMap {
		strIndexes := make([]string, 0, len(value))
		for _, index := range value {
			strIndexes = append(strIndexes, fmt.Sprintf("%d", index))
		}
		keyValues = append(keyValues, fmt.Sprintf(`["%d"]="%s"`, key, strings.Join(strIndexes, ",")))
	}

	slices.Sort(keyValues)

	return fmt.Sprintf(`#!/usr/bin/bash

set -o errexit
set -o nounset
set -o pipefail

# External
# JOB_COMPLETION_INDEX  - completion index of the job.
# JOB_CONTAINER_INDEX   - container index in the container template.

# ["COMPLETION_INDEX"]=CONTAINER_INDEX1,CONTAINER_INDEX2
declare -A ARRAY_INDEXES=(%[1]s) 	# Requires bash v4+

CONTAINER_INDEXES=${ARRAY_INDEXES[${JOB_COMPLETION_INDEX}]}
CONTAINER_INDEXES=(${CONTAINER_INDEXES//,/ })

if [[ ! -v CONTAINER_INDEXES[${JOB_CONTAINER_INDEX}] ]];
then
	exit 0
fi

# Generated on the builder
%[2]s
%[3]s

# To be supported later
# export SLURM_JOB_NODELIST=        # Contains the definition (list) of the nodes (actually pods) that is assigned to the job. To be supported later.
# export SLURM_NODELIST=            # Deprecated. Same as SLURM_JOB_NODELIST. To be supported later.
# export SLURM_NTASKS_PER_SOCKET    # Number of tasks requested per socket. To be supported later.
# export SLURM_NTASKS_PER_CORE      # Number of tasks requested per core. To be supported later.
# export SLURM_NTASKS_PER_GPU       # Number of tasks requested per GPU. To be supported later.

# Calculated variables in runtime
export SLURM_JOB_ID=$(( JOB_COMPLETION_INDEX * SLURM_TASKS_PER_NODE + JOB_CONTAINER_INDEX + SLURM_ARRAY_JOB_ID ))   # The Job ID.
export SLURM_JOBID=$SLURM_JOB_ID                                                                                    # Deprecated. Same as $SLURM_JOB_ID
export SLURM_ARRAY_TASK_ID=${CONTAINER_INDEXES[${JOB_CONTAINER_INDEX}]}												# Task ID.

# fill_file_name fills file name by pattern
# \\ - Do not process any of the replacement symbols.
# %%%% - The character "%%".
# %%A - Job array's master job allocation number (for now it is equivalent to SLURM_JOB_ID).
# %%a - Job array ID (index) number (SLURM_ARRAY_TASK_ID).
# %%j - jobid of the running job (SLURM_JOB_ID).
# %%N - short hostname (pod name).
# %%n - node(pod) identifier relative to current job - index from K8S index job.
# %%t - task identifier (rank) relative to current job - It is array id position.
# %%u - user name (from the client machine).
# %%x - job name.
fill_file_name () {
  REPLACED="$1"

  if [[ "$REPLACED" == "\\"* ]]; then
      REPLACED="${REPLACED//\\/}"
      echo "${REPLACED}"
      return 0
  fi

  REPLACED=$(echo "$REPLACED" | sed -E "s/(%%)(%%A)/\1\n\2/g;:a s/(^|[^\n])%%A/\1$SLURM_ARRAY_JOB_ID/;ta;s/\n//g")
  REPLACED=$(echo "$REPLACED" | sed -E "s/(%%)(%%a)/\1\n\2/g;:a s/(^|[^\n])%%a/\1$SLURM_ARRAY_TASK_ID/;ta;s/\n//g")
  REPLACED=$(echo "$REPLACED" | sed -E "s/(%%)(%%j)/\1\n\2/g;:a s/(^|[^\n])%%j/\1$SLURM_JOB_ID/;ta;s/\n//g")
  REPLACED=$(echo "$REPLACED" | sed -E "s/(%%)(%%N)/\1\n\2/g;:a s/(^|[^\n])%%N/\1$HOSTNAME/;ta;s/\n//g")
  REPLACED=$(echo "$REPLACED" | sed -E "s/(%%)(%%n)/\1\n\2/g;:a s/(^|[^\n])%%n/\1$JOB_COMPLETION_INDEX/;ta;s/\n//g")
  REPLACED=$(echo "$REPLACED" | sed -E "s/(%%)(%%t)/\1\n\2/g;:a s/(^|[^\n])%%t/\1$SLURM_ARRAY_TASK_ID/;ta;s/\n//g")
  REPLACED=$(echo "$REPLACED" | sed -E "s/(%%)(%%u)/\1\n\2/g;:a s/(^|[^\n])%%u/\1$USER_ID/;ta;s/\n//g")
  REPLACED=$(echo "$REPLACED" | sed -E "s/(%%)(%%x)/\1\n\2/g;:a s/(^|[^\n])%%x/\1$SBATCH_JOB_NAME/;ta;s/\n//g")

  REPLACED="${REPLACED//%%%%/%%}"

  echo "$REPLACED"
}

export SBATCH_INPUT=$(fill_file_name "$SBATCH_INPUT")
export SBATCH_OUTPUT=$(fill_file_name "$SBATCH_OUTPUT")
export SBATCH_ERROR=$(fill_file_name "$SBATCH_ERROR")

%[4]s
`,
		strings.Join(keyValues, " "), // %[1]s
		b.buildSbatchVariables(),     // %[2]s
		b.buildSlurmVariables(),      // %[3]s
		b.buildEntrypointCommand(),   // // %[4]s
	)
}

func (b *slurmBuilder) buildSbatchVariables() string {
	return fmt.Sprintf(`
export SBATCH_INPUT=%[1]s 		# Instruct Slurm to connect the batch script's standard input directly to the file name specified in the "filename pattern".
export SBATCH_OUTPUT=%[2]s		# Instruct Slurm to connect the batch script's standard output directly to the file name specified in the "filename pattern".
export SBATCH_ERROR=%[3]s		# Instruct Slurm to connect the batch script's standard error directly to the file name specified in the "filename pattern".
`,
		b.input,
		b.stdout,
		b.stderr,
	)
}

func (b *slurmBuilder) buildSlurmVariables() string {
	nTasks := ptr.Deref(b.nTasks, 1)
	nodes := ptr.Deref(b.nodes, 1)

	return fmt.Sprintf(`export SLURM_ARRAY_JOB_ID=%[1]d       		# Job array’s master job ID number.
export SLURM_ARRAY_TASK_COUNT=%[2]d  		# Total number of tasks in a job array.
export SLURM_ARRAY_TASK_MAX=%[3]d    		# Job array’s maximum ID (index) number.
export SLURM_ARRAY_TASK_MIN=%[4]d    		# Job array’s minimum ID (index) number.
export SLURM_TASKS_PER_NODE=%[5]d    		# Job array’s master job ID number.
export SLURM_CPUS_PER_TASK=%[6]s       		# Number of CPUs per task.
export SLURM_CPUS_ON_NODE=%[7]s        		# Number of CPUs on the allocated node (actually pod).
export SLURM_JOB_CPUS_PER_NODE=%[8]s   		# Count of processors available to the job on this node.
export SLURM_CPUS_PER_GPU=%[9]s        		# Number of CPUs requested per allocated GPU.
export SLURM_MEM_PER_CPU=%[10]s         	# Memory per CPU. Same as --mem-per-cpu .
export SLURM_MEM_PER_GPU=%[11]s         	# Memory per GPU.
export SLURM_MEM_PER_NODE=%[12]s        	# Memory per node. Same as --mem.
export SLURM_GPUS=%[13]s                	# Number of GPUs requested (in total).
export SLURM_NTASKS=%[14]d              	# Same as -n, –ntasks. The number of tasks.
export SLURM_NTASKS_PER_NODE=%[15]d  		# Number of tasks requested per node.
export SLURM_NPROCS=$SLURM_NTASKS       	# Same as -n, --ntasks. See $SLURM_NTASKS.
export SLURM_NNODES=%[16]d            		# Total number of nodes (actually pods) in the job’s resource allocation.
export SLURM_SUBMIT_DIR=%[17]s        		# The path of the job submission directory.
export SLURM_SUBMIT_HOST=$HOSTNAME       	# The hostname of the node used for job submission.
export SBATCH_JOB_NAME=%[18]s				# Specified job name.`,
		1,                      // %[1]d
		b.arrayIndexes.Count(), // %[2]d
		b.arrayIndexes.Max(),   // %[3]d
		b.arrayIndexes.Min(),   // %[4]d
		nTasks,                 // %[5]d
		"",                     // %[6]s
		"",                     // %[7]s
		"",                     // %[8]s
		"",                     // %[9]s
		"",                     // %[10]s
		"",                     // %[11]s
		"",                     // %[12]s
		"",                     // %[13]s
		nTasks,                 // %[14]d
		nTasks,                 // %[15]d
		nodes,                  // %[16]d
		slurmPath,              // %[17]s
		b.jobName,              // %[18]s
	)
}

func (b *slurmBuilder) buildEntrypointCommand() string {
	strBuilder := strings.Builder{}
	strBuilder.WriteString("bash")
	strBuilder.WriteByte(' ')
	strBuilder.WriteString(slurmPath)
	strBuilder.WriteByte('/')
	strBuilder.WriteString("script.sh")

	if b.input != "" {
		strBuilder.WriteString(" <$SBATCH_INPUT")
	}

	if b.stdout != "" {
		strBuilder.WriteString(" 1>$SBATCH_OUTPUT")
	}

	if b.stderr != "" {
		strBuilder.WriteString(" 2>$SBATCH_ERROR")
	}

	return strBuilder.String()
}

func (b *slurmBuilder) replaceCLIFlags(scriptFlags parser.ParsedSlurmFlags) {
	if len(scriptFlags.Array) != 0 {
		b.array = scriptFlags.Array
	}

	if scriptFlags.CpusPerTask != nil {
		b.cpusPerTask = scriptFlags.CpusPerTask
	}

	if scriptFlags.GpusPerTask != nil {
		b.gpusPerTask = scriptFlags.GpusPerTask
	}

	if scriptFlags.MemPerTask != nil {
		b.memPerTask = scriptFlags.MemPerTask
	}

	if scriptFlags.MemPerCPU != nil {
		b.memPerCPU = scriptFlags.MemPerCPU
	}

	if scriptFlags.MemPerGPU != nil {
		b.memPerGPU = scriptFlags.MemPerGPU
	}

	if scriptFlags.Nodes != nil {
		b.nodes = scriptFlags.Nodes
	}

	if scriptFlags.NTasks != nil {
		b.nTasks = scriptFlags.NTasks
	}

	if len(scriptFlags.StdOut) != 0 {
		b.stdout = scriptFlags.StdOut
	}

	if len(scriptFlags.StdErr) != 0 {
		b.stderr = scriptFlags.StdErr
	}

	if len(scriptFlags.Input) != 0 {
		b.input = scriptFlags.Input
	}

	if len(scriptFlags.JobName) != 0 {
		b.jobName = scriptFlags.JobName
	}

	if len(scriptFlags.JobName) != 0 {
		b.partition = scriptFlags.Partition
	}
}

func newSlurmBuilder(b *Builder) *slurmBuilder {
	return &slurmBuilder{Builder: b}
}
