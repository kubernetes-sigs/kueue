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
)

const (
	slurmScriptFilename     = "script.sh"
	slurmEntrypointFilename = "entrypoint.sh"
	slurmPath               = "/slurm"
)

type slurmBuilder struct {
	*Builder

	arrayIndexes arrayIndexes
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
	objectName := b.generatePrefixName() + utilrand.String(5)
	job.ObjectMeta.GenerateName = ""
	job.ObjectMeta.Name = objectName

	content, err := os.ReadFile(b.script)
	if err != nil {
		return nil, err
	}

	if b.array == "" {
		b.arrayIndexes = generateArrayIndexes(ptr.Deref(b.nodes, 1))
	} else {
		b.arrayIndexes, err = parseArrayIndexes(b.array)
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
	nTasks := ptr.Deref(b.nTasks, 1)

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

# COMPLETION_INDEX=CONTAINER_INDEX1,CONTAINER_INDEX2
declare -A array_indexes=(%[1]s) 	# Requires bash 4+

container_indexes=${array_indexes[${JOB_COMPLETION_INDEX}]}
container_indexes=(${container_indexes//,/ })

if [[ ! -v container_indexes[${JOB_CONTAINER_INDEX}] ]];
then
	exit 0
fi

# Generated on the builder
export SLURM_ARRAY_JOB_ID=1       			# Job array’s master job ID number.
export SLURM_ARRAY_TASK_COUNT=%[2]d  		# Total number of tasks in a job array.
export SLURM_ARRAY_TASK_MAX=%[3]d    		# Job array’s maximum ID (index) number.
export SLURM_ARRAY_TASK_MIN=%[4]d    		# Job array’s minimum ID (index) number.
export SLURM_TASKS_PER_NODE=%[5]d    		# Job array’s master job ID number.
export SLURM_CPUS_PER_TASK=       			# Number of CPUs per task.
export SLURM_CPUS_ON_NODE=        			# Number of CPUs on the allocated node (actually pod).
export SLURM_JOB_CPUS_PER_NODE=   			# Count of processors available to the job on this node.
export SLURM_CPUS_PER_GPU=        			# Number of CPUs requested per allocated GPU.
export SLURM_MEM_PER_CPU=         			# Memory per CPU. Same as --mem-per-cpu .
export SLURM_MEM_PER_GPU=         			# Memory per GPU.
export SLURM_MEM_PER_NODE=        			# Memory per node. Same as --mem.
export SLURM_GPUS=                			# Number of GPUs requested (in total).
export SLURM_NTASKS=%[6]d              		# Same as -n, –ntasks. The number of tasks.
export SLURM_NTASKS_PER_NODE=$SLURM_NTASKS  # Number of tasks requested per node.
export SLURM_NPROCS=$SLURM_NTASKS       	# Same as -n, --ntasks. See $SLURM_NTASKS.
export SLURM_NNODES=%[7]d            		# Total number of nodes (actually pods) in the job’s resource allocation.
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

bash %[8]s/script.sh
`,
		strings.Join(keyValues, " "),
		b.arrayIndexes.Count(),
		b.arrayIndexes.Max(),
		b.arrayIndexes.Min(),
		nTasks,
		nTasks,
		ptr.Deref(b.nodes, 1),
		slurmPath,
	)
}

func newSlurmBuilder(b *Builder) *slurmBuilder {
	return &slurmBuilder{Builder: b}
}
