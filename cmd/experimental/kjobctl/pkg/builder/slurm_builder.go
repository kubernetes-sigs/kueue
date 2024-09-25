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
	"math"
	"os"
	"slices"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/parser"
)

const (
	// Job array's master job ID number.
	// Job IDs start at 1, so the master job ID will always be 1.
	slurmArrayJobID = 1

	slurmScriptsPath           = "/slurm/scripts"
	slurmEnvEntrypointFilename = "init-entrypoint.sh"
	slurmEntrypointFilename    = "entrypoint.sh"
	slurmScriptFilename        = "script"

	slurmEnvsPath          = "/slurm/env"
	slurmSbatchEnvFilename = "sbatch.env"
	slurmSlurmEnvFilename  = "slurm.env"

	slurmInitImage = "bash:5-alpine3.20"

	//# \\ - Do not process any of the replacement symbols.
	//# %% - The character "%".
	//# %A - Job array's master job allocation number (for now it is equivalent to SLURM_JOB_ID).
	//# %a - Job array ID (index) number (SLURM_ARRAY_TASK_ID).
	//# %j - job id of the running job (SLURM_JOB_ID).
	//# %N - short hostname (pod name).
	//# %n - node(pod) identifier relative to current job - index from K8S index job.
	//# %t - task identifier (rank) relative to current job - It is array id position.
	//# %u - username (from the client machine).
	//# %x - job name.
	unmaskFilenameFunction = `unmask_filename () {
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
}`
)

var (
	noScriptSpecifiedErr = errors.New("no script specified")
)

var (
	slurmInitEntrypointFilenamePath = fmt.Sprintf("%s/%s", slurmScriptsPath, slurmEnvEntrypointFilename)
	slurmEntrypointFilenamePath     = fmt.Sprintf("%s/%s", slurmScriptsPath, slurmEntrypointFilename)
	slurmScriptFilenamePath         = fmt.Sprintf("%s/%s", slurmScriptsPath, slurmScriptFilename)
)

type slurmBuilder struct {
	*Builder

	scriptContent   string
	arrayIndexes    parser.ArrayIndexes
	cpusOnNode      *resource.Quantity
	cpusPerGpu      *resource.Quantity
	totalMemPerNode *resource.Quantity
	totalGpus       int
}

var _ builder = (*slurmBuilder)(nil)

func (b *slurmBuilder) validateGeneral() error {
	if len(b.script) == 0 {
		return noScriptSpecifiedErr
	}
	return nil
}

func (b *slurmBuilder) complete() error {
	content, err := os.ReadFile(b.script)
	if err != nil {
		return err
	}
	b.scriptContent = string(content)

	if err := b.getSbatchEnvs(); err != nil {
		return err
	}

	if err := b.replaceScriptFlags(); err != nil {
		return err
	}

	if err := b.validateMutuallyExclusiveFlags(); err != nil {
		return err
	}

	if b.array == "" {
		b.arrayIndexes = parser.GenerateArrayIndexes(ptr.Deref(b.nodes, 1) * ptr.Deref(b.nTasks, 1))
	} else {
		b.arrayIndexes, err = parser.ParseArrayIndexes(b.array)
		if err != nil {
			return err
		}
		if b.arrayIndexes.Parallelism != nil {
			b.nodes = b.arrayIndexes.Parallelism
		}
	}

	return nil
}

func (b *slurmBuilder) validateMutuallyExclusiveFlags() error {
	flags := map[string]bool{
		string(v1alpha1.MemPerTaskFlag): b.memPerTask != nil,
		string(v1alpha1.MemPerCPUFlag):  b.memPerCPU != nil,
		string(v1alpha1.MemPerGPUFlag):  b.memPerGPU != nil,
	}

	var setFlagsCount int
	setFlags := make([]string, 0)
	for f, isSet := range flags {
		if isSet {
			setFlagsCount++
			setFlags = append(setFlags, f)
		}
	}

	if setFlagsCount > 1 {
		return fmt.Errorf(
			"if any flags in the group [%s %s %s] are set none of the others can be; [%s] were all set",
			v1alpha1.MemPerTaskFlag,
			v1alpha1.MemPerGPUFlag,
			v1alpha1.MemPerGPUFlag,
			strings.Join(setFlags, " "),
		)
	}

	return nil
}

func (b *slurmBuilder) build(ctx context.Context) (runtime.Object, []runtime.Object, error) {
	if err := b.validateGeneral(); err != nil {
		return nil, nil, err
	}

	if err := b.complete(); err != nil {
		return nil, nil, err
	}

	template, err := b.kjobctlClientset.KjobctlV1alpha1().JobTemplates(b.profile.Namespace).
		Get(ctx, string(b.mode.Template), metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}

	job := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Job",
			APIVersion: "batch/v1",
		},
		ObjectMeta: b.buildObjectMeta(template.Template.ObjectMeta),
		Spec:       template.Template.Spec,
	}
	job.Spec.CompletionMode = ptr.To(batchv1.IndexedCompletion)

	objectName := b.generatePrefixName() + utilrand.String(5)
	job.ObjectMeta.GenerateName = ""
	job.ObjectMeta.Name = objectName

	configMap := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: b.buildObjectMeta(template.Template.ObjectMeta),
		Data: map[string]string{
			slurmEnvEntrypointFilename: b.buildInitEntrypointScript(),
			slurmEntrypointFilename:    b.buildEntrypointScript(),
			slurmScriptFilename:        b.scriptContent,
		},
	}
	configMap.ObjectMeta.GenerateName = ""
	configMap.ObjectMeta.Name = objectName

	b.buildPodSpecVolumesAndEnv(&job.Spec.Template.Spec)
	job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes,
		corev1.Volume{
			Name: "slurm-scripts",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: configMap.Name,
					},
					Items: []corev1.KeyToPath{
						{
							Key:  slurmEnvEntrypointFilename,
							Path: slurmEnvEntrypointFilename,
						},
						{
							Key:  slurmEntrypointFilename,
							Path: slurmEntrypointFilename,
						},
						{
							Key:  slurmScriptFilename,
							Path: slurmScriptFilename,
							Mode: ptr.To[int32](0755),
						},
					},
				},
			},
		},
		corev1.Volume{
			Name: "slurm-env",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	)

	job.Spec.Template.Spec.InitContainers = append(job.Spec.Template.Spec.InitContainers, corev1.Container{
		Name:    "slurm-init-env",
		Image:   slurmInitImage,
		Command: []string{"bash", slurmInitEntrypointFilenamePath},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "slurm-scripts",
				MountPath: slurmScriptsPath,
			},
			{
				Name:      "slurm-env",
				MountPath: slurmEnvsPath,
			},
		},
	})

	gpusPerTask, err := resource.ParseQuantity("0")
	if err != nil {
		return nil, nil, errors.New("error initializing gpus counter")
	}
	for _, number := range b.gpusPerTask {
		gpusPerTask.Add(*number)
	}

	for i := range job.Spec.Template.Spec.Containers {
		container := &job.Spec.Template.Spec.Containers[i]

		container.Command = []string{"bash", slurmEntrypointFilenamePath}

		var requests corev1.ResourceList
		if b.requests != nil {
			requests = b.requests
		} else {
			requests = corev1.ResourceList{}
		}

		if b.cpusPerTask != nil {
			requests[corev1.ResourceCPU] = *b.cpusPerTask
		}

		if b.gpusPerTask != nil {
			for name, number := range b.gpusPerTask {
				requests[corev1.ResourceName(name)] = *number
			}
		}

		if b.memPerTask != nil {
			requests[corev1.ResourceMemory] = *b.memPerTask
		}

		if b.memPerCPU != nil && b.cpusPerTask != nil {
			memPerCPU := *b.memPerCPU
			memPerCPU.Mul(b.cpusPerTask.Value())
			requests[corev1.ResourceMemory] = memPerCPU
		}

		if b.memPerGPU != nil && b.gpusPerTask != nil {
			memPerGpu := *b.memPerGPU
			memPerGpu.Mul(gpusPerTask.Value())
			requests[corev1.ResourceMemory] = memPerGpu
		}

		if len(requests) > 0 {
			container.Resources.Requests = b.requests
		}

		container.VolumeMounts = append(container.VolumeMounts,
			corev1.VolumeMount{
				Name:      "slurm-scripts",
				MountPath: slurmScriptsPath,
			},
			corev1.VolumeMount{
				Name:      "slurm-env",
				MountPath: slurmEnvsPath,
			},
		)
	}

	nTasks := ptr.Deref(b.nTasks, 1)
	completions := int32(math.Ceil(float64(b.arrayIndexes.Count()) / float64(nTasks)))

	job.Spec.Completions = ptr.To(completions)
	job.Spec.Parallelism = b.nodes

	if nTasks > 1 {
		for i := 1; i < int(nTasks); i++ {
			job.Spec.Template.Spec.Containers = append(job.Spec.Template.Spec.Containers, job.Spec.Template.Spec.Containers[0])
		}

		for i := range nTasks {
			job.Spec.Template.Spec.Containers[i].Name =
				fmt.Sprintf("%s-%d", job.Spec.Template.Spec.Containers[i].Name, i)
		}
	}

	for i := range job.Spec.Template.Spec.Containers {
		job.Spec.Template.Spec.Containers[i].Env = append(job.Spec.Template.Spec.Containers[i].Env, corev1.EnvVar{
			Name:  "JOB_CONTAINER_INDEX",
			Value: strconv.FormatInt(int64(i), 10),
		})
	}

	if b.nodes != nil {
		job.Spec.Parallelism = b.nodes
	}

	if b.cpusPerTask != nil {
		b.cpusOnNode = ptr.To(b.cpusPerTask.DeepCopy())
		b.cpusOnNode.Mul(int64(len(job.Spec.Template.Spec.Containers)))
	}

	if b.memPerCPU != nil {
		b.totalMemPerNode = ptr.To(b.memPerCPU.DeepCopy())
		b.totalMemPerNode.Mul(int64(len(job.Spec.Template.Spec.Containers)))
	}

	totalGpus := gpusPerTask
	totalTasks := int64(len(job.Spec.Template.Spec.Containers))
	totalGpus.Mul(totalTasks)
	b.totalGpus = int(totalGpus.Value())

	if b.totalGpus > 0 {
		cpusPerGpu := b.cpusOnNode.Value() / int64(b.totalGpus)
		b.cpusPerGpu = resource.NewQuantity(cpusPerGpu, resource.DecimalSI)
	}

	return job, []runtime.Object{configMap}, nil
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

func (b *slurmBuilder) buildInitEntrypointScript() string {
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

	var gpusPerTask, memPerCPU, memPerGPU string
	if b.gpusPerTask != nil {
		gpus := make([]string, 0)
		for name, number := range b.gpusPerTask {
			gpus = append(gpus, fmt.Sprintf("%s:%s", name, number))
		}
		gpusPerTask = strings.Join(gpus, ",")
	}
	if b.memPerCPU != nil {
		memPerCPU = b.memPerCPU.String()
	}
	if b.memPerGPU != nil {
		memPerGPU = b.memPerGPU.String()
	}

	return fmt.Sprintf(`#!/usr/local/bin/bash

set -o errexit
set -o nounset
set -o pipefail

# External variables
# JOB_COMPLETION_INDEX  - completion index of the job.

for i in {0..%[1]d}
do
	# ["COMPLETION_INDEX"]="CONTAINER_INDEX_1,CONTAINER_INDEX_2"
	declare -A array_indexes=(%[2]s) 	# Requires bash v4+
	
	container_indexes=${array_indexes[${JOB_COMPLETION_INDEX}]}
	container_indexes=(${container_indexes//,/ })
	
	if [[ ! -v container_indexes[$i] ]];
	then
		break
	fi

	mkdir -p %[3]s/$i
	
	cat << EOF > %[3]s/$i/%[4]s 
SBATCH_ARRAY_INX=%[6]s
SBATCH_GPUS_PER_TASK=%[7]s
SBATCH_MEM_PER_CPU=%[8]s
SBATCH_MEM_PER_GPU=%[9]s
SBATCH_OUTPUT=%[10]s
SBATCH_ERROR=%[11]s
SBATCH_INPUT=%[12]s
SBATCH_JOB_NAME=%[13]s
SBATCH_PARTITION=%[14]s
EOF
	
	cat << EOF > %[3]s/$i/%[5]s
SLURM_ARRAY_JOB_ID=%[15]d	
SLURM_ARRAY_TASK_COUNT=%[16]d
SLURM_ARRAY_TASK_MAX=%[17]d
SLURM_ARRAY_TASK_MIN=%[18]d
SLURM_TASKS_PER_NODE=%[1]d
SLURM_CPUS_PER_TASK=%[19]s
SLURM_CPUS_ON_NODE=%[20]s
SLURM_JOB_CPUS_PER_NODE=%[21]s
SLURM_CPUS_PER_GPU=%[22]s
SLURM_MEM_PER_CPU=%[23]s
SLURM_MEM_PER_GPU=%[24]s
SLURM_MEM_PER_NODE=%[25]s
SLURM_GPUS=%[26]d
SLURM_NTASKS=%[1]d
SLURM_NTASKS_PER_NODE=%[1]d
SLURM_NPROCS=%[1]d
SLURM_NNODES=%[27]d
SLURM_SUBMIT_DIR=%[28]s
SLURM_SUBMIT_HOST=$HOSTNAME
SLURM_JOB_ID=$(( JOB_COMPLETION_INDEX * %[1]d + i + %[15]d ))
SLURM_JOBID=$(( JOB_COMPLETION_INDEX * %[1]d + i + %[15]d ))
SLURM_ARRAY_TASK_ID=${container_indexes[$i]}
EOF

done
`,
		ptr.Deref(b.nTasks, 1),       // %[1]s
		strings.Join(keyValues, " "), // %[2]s

		slurmEnvsPath,          // %[3]s
		slurmSbatchEnvFilename, // %[4]s
		slurmSlurmEnvFilename,  // %[5]s

		// Sbatch variables
		b.array,     // %[6]s
		gpusPerTask, // %[7]s
		memPerCPU,   // %[8]s
		memPerGPU,   // %[9]s
		b.output,    // %[10]s
		b.error,     // %[11]s
		b.input,     // %[12]s
		b.jobName,   // %[13]s
		b.partition, // %[14]s

		// Slurm variables
		slurmArrayJobID,                    // %[15]d
		b.arrayIndexes.Count(),             // %[16]d
		b.arrayIndexes.Max(),               // %[17]d
		b.arrayIndexes.Min(),               // %[18]d
		getValueOrEmpty(b.cpusPerTask),     // %[19]s
		getValueOrEmpty(b.cpusOnNode),      // %[20]s
		getValueOrEmpty(b.cpusOnNode),      // %[21]s
		getValueOrEmpty(b.cpusPerGpu),      // %[22]s
		getValueOrEmpty(b.memPerCPU),       // %[23]s
		getValueOrEmpty(b.memPerGPU),       // %[24]s
		getValueOrEmpty(b.totalMemPerNode), // %[25]s
		b.totalGpus,                        // %[26]s
		ptr.Deref(b.nodes, 1),              // %[27]d
		slurmScriptsPath,                   // %[28]s
	)
}

func (b *slurmBuilder) buildEntrypointScript() string {
	return fmt.Sprintf(`#!/usr/local/bin/bash

set -o errexit
set -o nounset
set -o pipefail

# External variables
# JOB_CONTAINER_INDEX 	- container index in the container template.

if [ ! -d "%[1]s/$JOB_CONTAINER_INDEX" ]; then
	exit 0
fi

source %[1]s/$JOB_CONTAINER_INDEX/%[2]s

export $(cat %[1]s/$JOB_CONTAINER_INDEX/%[3]s | xargs)

%[4]s

input_file=$(unmask_filename "$SBATCH_INPUT")
output_file=$(unmask_filename "$SBATCH_OUTPUT")
error_path=$(unmask_filename "$SBATCH_ERROR")

%[5]s
`,
		slurmEnvsPath,              // %[1]s
		slurmSbatchEnvFilename,     // %[2]s
		slurmSlurmEnvFilename,      // %[3]s
		unmaskFilenameFunction,     // %[4]s
		b.buildEntrypointCommand(), // %[5]s
	)
}

func getValueOrEmpty(ptr *resource.Quantity) string {
	if ptr != nil {
		return ptr.String()
	}

	return ""
}

func (b *slurmBuilder) buildEntrypointCommand() string {
	strBuilder := strings.Builder{}

	strBuilder.WriteString(slurmScriptFilenamePath)

	if b.input != "" {
		strBuilder.WriteString(" <$input_file")
	}

	if b.output != "" {
		strBuilder.WriteString(" 1>$output_file")
	}

	if b.error != "" {
		strBuilder.WriteString(" 2>$error_file")
	}

	return strBuilder.String()
}

func (b *slurmBuilder) getSbatchEnvs() error {
	if len(b.array) == 0 {
		b.array = os.Getenv("SBATCH_ARRAY_INX")
	}

	if b.gpusPerTask == nil {
		if env, ok := os.LookupEnv("SBATCH_GPUS_PER_TASK"); ok {
			val, err := parser.GpusFlag(env)
			if err != nil {
				return fmt.Errorf("cannot parse '%s': %w", env, err)
			}
			b.gpusPerTask = val
		}
	}

	if b.memPerTask == nil {
		if env, ok := os.LookupEnv("SBATCH_MEM_PER_CPU"); ok {
			val, err := resource.ParseQuantity(env)
			if err != nil {
				return fmt.Errorf("cannot parse '%s': %w", env, err)
			}
			b.memPerTask = ptr.To(val)
		}
	}

	if b.memPerGPU == nil {
		if env, ok := os.LookupEnv("SBATCH_MEM_PER_GPU"); ok {
			val, err := resource.ParseQuantity(env)
			if err != nil {
				return fmt.Errorf("cannot parse '%s': %w", env, err)
			}
			b.memPerGPU = ptr.To(val)
		}
	}

	if len(b.output) == 0 {
		b.output = os.Getenv("SBATCH_OUTPUT")
	}

	if len(b.error) == 0 {
		b.error = os.Getenv("SBATCH_ERROR")
	}

	if len(b.input) == 0 {
		b.input = os.Getenv("SBATCH_INPUT")
	}

	if len(b.jobName) == 0 {
		b.jobName = os.Getenv("SBATCH_JOB_NAME")
	}

	if len(b.partition) == 0 {
		b.partition = os.Getenv("SBATCH_PARTITION")
	}

	return nil
}

func (b *slurmBuilder) replaceScriptFlags() error {
	scriptFlags, err := parser.SlurmFlags(b.scriptContent, b.ignoreUnknown)
	if err != nil {
		return err
	}

	if len(b.array) == 0 {
		b.array = scriptFlags.Array
	}

	if b.cpusPerTask == nil {
		b.cpusPerTask = scriptFlags.CpusPerTask
	}

	if b.gpusPerTask == nil {
		b.gpusPerTask = scriptFlags.GpusPerTask
	}

	if b.memPerTask == nil {
		b.memPerTask = scriptFlags.MemPerTask
	}

	if b.memPerCPU == nil {
		b.memPerCPU = scriptFlags.MemPerCPU
	}

	if b.memPerGPU == nil {
		b.memPerGPU = scriptFlags.MemPerGPU
	}

	if b.nodes == nil {
		b.nodes = scriptFlags.Nodes
	}

	if b.nTasks == nil {
		b.nTasks = scriptFlags.NTasks
	}

	if len(b.output) == 0 {
		b.output = scriptFlags.Output
	}

	if len(b.error) == 0 {
		b.error = scriptFlags.Error
	}

	if len(b.input) == 0 {
		b.input = scriptFlags.Input
	}

	if len(b.jobName) == 0 {
		b.jobName = scriptFlags.JobName
	}

	if len(b.partition) == 0 {
		b.partition = scriptFlags.Partition
	}

	return nil
}

func newSlurmBuilder(b *Builder) *slurmBuilder {
	return &slurmBuilder{Builder: b}
}
