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
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"text/template"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/parser"
)

//go:embed templates/slurm_*
var slurmTemplates embed.FS

const (
	// Note that the first job ID will always be 1.
	slurmArrayJobID = 1

	slurmScriptsPath            = "/slurm/scripts"
	slurmInitEntrypointFilename = "init-entrypoint.sh"
	slurmEntrypointFilename     = "entrypoint.sh"
	slurmScriptFilename         = "script"

	slurmEnvsPath         = "/slurm/env"
	slurmSlurmEnvFilename = "slurm.env"
)

var (
	noScriptSpecifiedErr = errors.New("no script specified")
)

var (
	slurmInitEntrypointFilenamePath = fmt.Sprintf("%s/%s", slurmScriptsPath, slurmInitEntrypointFilename)
	slurmEntrypointFilenamePath     = fmt.Sprintf("%s/%s", slurmScriptsPath, slurmEntrypointFilename)
	slurmScriptFilenamePath         = fmt.Sprintf("%s/%s", slurmScriptsPath, slurmScriptFilename)

	unmaskReplacer = strings.NewReplacer(
		"%%", "%",
		"%A", "${SLURM_ARRAY_JOB_ID}",
		"%a", "${SLURM_ARRAY_TASK_ID}",
		"%j", "${SLURM_JOB_ID}",
		"%N", "${HOSTNAME}",
		"%n", "${JOB_COMPLETION_INDEX}",
		"%t", "${SLURM_ARRAY_TASK_ID}",
		"%u", "${USER_ID}",
		"%x", "${SBATCH_JOB_NAME}",
	)
)

type slurmBuilder struct {
	*Builder

	scriptContent   string
	template        *template.Template
	arrayIndexes    parser.ArrayIndexes
	cpusOnNode      *resource.Quantity
	cpusPerGpu      *resource.Quantity
	totalMemPerNode *resource.Quantity
	totalGpus       *resource.Quantity
}

var _ builder = (*slurmBuilder)(nil)

func (b *slurmBuilder) validateGeneral() error {
	if len(b.script) == 0 {
		return noScriptSpecifiedErr
	}

	if b.memPerCPU != nil && b.cpusPerTask == nil {
		return noCpusPerTaskSpecifiedErr
	}

	if b.memPerGPU != nil && b.gpusPerTask == nil {
		return noGpusPerTaskSpecifiedErr
	}

	return nil
}

func (b *slurmBuilder) complete() error {
	content, err := os.ReadFile(b.script)
	if err != nil {
		return err
	}
	b.scriptContent = string(content)

	t, err := template.ParseFS(slurmTemplates, "templates/*")
	if err != nil {
		return err
	}
	b.template = t

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
		string(v1alpha1.MemPerNodeFlag): b.memPerNode != nil,
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

	objectMeta, err := b.buildObjectMeta(template.Template.ObjectMeta, true)
	if err != nil {
		return nil, nil, err
	}

	job := &batchv1.Job{
		TypeMeta:   metav1.TypeMeta{Kind: "Job", APIVersion: "batch/v1"},
		ObjectMeta: objectMeta,
		Spec:       template.Template.Spec,
	}

	job.Spec.CompletionMode = ptr.To(batchv1.IndexedCompletion)
	job.Spec.Template.Spec.Subdomain = job.Name

	b.buildPodSpecVolumesAndEnv(&job.Spec.Template.Spec)
	job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes,
		corev1.Volume{
			Name: "slurm-scripts",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: job.Name,
					},
					Items: []corev1.KeyToPath{
						{
							Key:  slurmInitEntrypointFilename,
							Path: slurmInitEntrypointFilename,
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
		Image:   b.initImage,
		Command: []string{"sh", slurmInitEntrypointFilenamePath},
		Env: []corev1.EnvVar{
			{
				Name: "POD_IP",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"},
				},
			},
		},
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

	var totalGPUsPerTask resource.Quantity
	for _, number := range b.gpusPerTask {
		totalGPUsPerTask.Add(*number)
	}

	var memPerCPU, memPerGPU, memPerContainer resource.Quantity
	if b.memPerCPU != nil && b.cpusPerTask != nil {
		memPerCPU = *b.memPerCPU
		memPerCPU.Mul(b.cpusPerTask.Value())
	}

	if b.memPerGPU != nil && b.gpusPerTask != nil {
		memPerGPU = *b.memPerGPU
		memPerGPU.Mul(totalGPUsPerTask.Value())
	}

	if b.memPerNode != nil {
		mem := b.memPerNode.MilliValue() / int64(len(job.Spec.Template.Spec.Containers))
		memPerContainer = *resource.NewMilliQuantity(mem, b.memPerNode.Format)
	}

	var totalCpus, totalGpus, totalMem resource.Quantity
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
			totalCpus.Add(*b.cpusPerTask)
		}

		if b.gpusPerTask != nil {
			for name, number := range b.gpusPerTask {
				requests[corev1.ResourceName(name)] = *number
			}
			totalGpus.Add(totalGPUsPerTask)
		}

		if b.memPerTask != nil {
			requests[corev1.ResourceMemory] = *b.memPerTask
			totalMem.Add(*b.memPerTask)
		}

		if !memPerCPU.IsZero() {
			requests[corev1.ResourceMemory] = memPerCPU
			totalMem.Add(memPerCPU)
		}

		if !memPerGPU.IsZero() {
			requests[corev1.ResourceMemory] = memPerGPU
			totalMem.Add(memPerGPU)
		}

		if len(requests) > 0 {
			container.Resources.Requests = requests
		}

		limits := corev1.ResourceList{}
		if !memPerContainer.IsZero() {
			limits[corev1.ResourceMemory] = memPerContainer
		}

		if len(limits) > 0 {
			container.Resources.Limits = limits
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
			replica := job.Spec.Template.Spec.Containers[0].DeepCopy()
			replica.Name = fmt.Sprintf("%s-%d", job.Spec.Template.Spec.Containers[0].Name, i)
			job.Spec.Template.Spec.Containers = append(job.Spec.Template.Spec.Containers, *replica)

			if b.cpusPerTask != nil {
				totalCpus.Add(*b.cpusPerTask)
			}

			if !memPerCPU.IsZero() {
				totalMem.Add(memPerCPU)
			}

			if !memPerGPU.IsZero() {
				totalMem.Add(memPerGPU)
			}
		}

		job.Spec.Template.Spec.Containers[0].Name = fmt.Sprintf("%s-0", job.Spec.Template.Spec.Containers[0].Name)
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

	if !totalCpus.IsZero() {
		b.cpusOnNode = &totalCpus
	}

	if !totalGpus.IsZero() {
		b.totalGpus = &totalGpus
	}

	if b.memPerNode != nil {
		b.totalMemPerNode = b.memPerNode
	} else if !totalMem.IsZero() {
		b.totalMemPerNode = &totalMem
	}

	if b.cpusOnNode != nil && b.totalGpus != nil && !totalGpus.IsZero() {
		cpusPerGpu := totalCpus.MilliValue() / totalGpus.MilliValue()
		b.cpusPerGpu = resource.NewQuantity(cpusPerGpu, b.cpusOnNode.Format)
	}

	initEntrypointScript, err := b.buildInitEntrypointScript(job.Name)
	if err != nil {
		return nil, nil, err
	}

	entrypointScript, err := b.buildEntrypointScript()
	if err != nil {
		return nil, nil, err
	}

	configMap := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{Kind: "ConfigMap", APIVersion: "v1"},
		ObjectMeta: b.buildChildObjectMeta(job.Name),
		Data: map[string]string{
			slurmInitEntrypointFilename: initEntrypointScript,
			slurmEntrypointFilename:     entrypointScript,
			slurmScriptFilename:         b.scriptContent,
		},
	}

	service := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{Kind: "Service", APIVersion: "v1"},
		ObjectMeta: b.buildChildObjectMeta(job.Name),
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Selector: map[string]string{
				"job-name": job.Name,
			},
		},
	}

	return job, []runtime.Object{configMap, service}, nil
}

func (b *slurmBuilder) buildArrayIndexes() string {
	nTasks := ptr.Deref(b.nTasks, 1)
	length := int64(math.Ceil(float64(len(b.arrayIndexes.Indexes)) / float64(nTasks)))
	containerIndexes := make([][]string, length)

	var (
		completionIndex int32
		containerIndex  int32
	)
	for _, index := range b.arrayIndexes.Indexes {
		containerIndexes[completionIndex] = append(containerIndexes[completionIndex], fmt.Sprint(index))
		containerIndex++
		if containerIndex >= nTasks {
			containerIndex = 0
			completionIndex++
		}
	}

	completionIndexes := make([]string, length)
	for completionIndex, containerIndexes := range containerIndexes {
		completionIndexes[completionIndex] = strings.Join(containerIndexes, ",")
	}

	return strings.Join(completionIndexes, ";")
}

type slurmInitEntrypointScript struct {
	ArrayIndexes string

	JobName   string
	Namespace string

	EnvsPath         string
	SlurmEnvFilename string

	SlurmArrayJobID     int32
	SlurmArrayTaskCount int32
	SlurmArrayTaskMax   int32
	SlurmArrayTaskMin   int32
	SlurmTasksPerNode   int32
	SlurmCPUsPerTask    string
	SlurmCPUsOnNode     string
	SlurmJobCPUsPerNode string
	SlurmCPUsPerGPU     string
	SlurmMemPerCPU      string
	SlurmMemPerGPU      string
	SlurmMemPerNode     string
	SlurmGPUs           string
	SlurmNTasks         int32
	SlurmNTasksPerNode  int32
	SlurmNProcs         int32
	SlurmNNodes         int32
	SlurmSubmitDir      string
	SlurmJobNodeList    string
	SlurmJobFirstNode   string

	FirstNodeIP               bool
	FirstNodeIPTimeoutSeconds int32
}

func (b *slurmBuilder) buildInitEntrypointScript(jobName string) (string, error) {
	nTasks := ptr.Deref(b.nTasks, 1)
	nodes := ptr.Deref(b.nodes, 1)

	nodeList := make([]string, nodes)
	for i := int32(0); i < nodes; i++ {
		nodeList[i] = fmt.Sprintf("%s-%d.%s", jobName, i, jobName)
	}

	scriptValues := slurmInitEntrypointScript{
		ArrayIndexes: b.buildArrayIndexes(),

		JobName:   jobName,
		Namespace: b.namespace,

		EnvsPath:         slurmEnvsPath,
		SlurmEnvFilename: slurmSlurmEnvFilename,

		SlurmArrayJobID:     slurmArrayJobID,
		SlurmArrayTaskCount: int32(b.arrayIndexes.Count()),
		SlurmArrayTaskMax:   b.arrayIndexes.Max(),
		SlurmArrayTaskMin:   b.arrayIndexes.Min(),
		SlurmTasksPerNode:   nTasks,
		SlurmCPUsPerTask:    getValueOrEmpty(b.cpusPerTask),
		SlurmCPUsOnNode:     getValueOrEmpty(b.cpusOnNode),
		SlurmJobCPUsPerNode: getValueOrEmpty(b.cpusOnNode),
		SlurmCPUsPerGPU:     getValueOrEmpty(b.cpusPerGpu),
		SlurmMemPerCPU:      getValueOrEmpty(b.memPerCPU),
		SlurmMemPerGPU:      getValueOrEmpty(b.memPerGPU),
		SlurmMemPerNode:     getValueOrEmpty(b.totalMemPerNode),
		SlurmGPUs:           getValueOrEmpty(b.totalGpus),
		SlurmNTasks:         nTasks,
		SlurmNTasksPerNode:  nTasks,
		SlurmNProcs:         nTasks,
		SlurmNNodes:         nodes,
		SlurmSubmitDir:      slurmScriptsPath,
		SlurmJobNodeList:    strings.Join(nodeList, ","),
		SlurmJobFirstNode:   nodeList[0],

		FirstNodeIP:               b.firstNodeIP,
		FirstNodeIPTimeoutSeconds: int32(b.firstNodeIPTimeout.Seconds()),
	}

	var script bytes.Buffer

	if err := b.template.ExecuteTemplate(&script, "slurm_init_entrypoint_script.sh.tmpl", scriptValues); err != nil {
		return "", err
	}

	return script.String(), nil
}

type slurmEntrypointScript struct {
	EnvsPath               string
	SbatchJobName          string
	SlurmEnvFilename       string
	ChangeDir              string
	BuildEntrypointCommand string
}

func (b *slurmBuilder) buildEntrypointScript() (string, error) {
	scriptValues := slurmEntrypointScript{
		EnvsPath:               slurmEnvsPath,
		SbatchJobName:          b.jobName,
		SlurmEnvFilename:       slurmSlurmEnvFilename,
		ChangeDir:              b.changeDir,
		BuildEntrypointCommand: b.buildEntrypointCommand(),
	}

	var script bytes.Buffer

	if err := b.template.ExecuteTemplate(&script, "slurm_entrypoint_script.sh.tmpl", scriptValues); err != nil {
		return "", err
	}

	return script.String(), nil
}

func getValueOrEmpty(ptr *resource.Quantity) string {
	if ptr != nil {
		return ptr.String()
	}

	return ""
}

// unmaskFilename unmasks a filename based on the filename pattern.
// For more details, see https://slurm.schedmd.com/sbatch.html#SECTION_FILENAME-PATTERN.
func unmaskFilename(filename string) string {
	if strings.Contains(filename, "\\\\") {
		return strings.ReplaceAll(filename, "\\\\", "")
	}
	return unmaskReplacer.Replace(filename)
}

func (b *slurmBuilder) buildEntrypointCommand() string {
	strBuilder := strings.Builder{}

	strBuilder.WriteString(slurmScriptFilenamePath)

	if b.input != "" {
		strBuilder.WriteString(" <")
		strBuilder.WriteString(unmaskFilename(b.input))
	}

	if b.output != "" {
		strBuilder.WriteString(" 1>")
		strBuilder.WriteString(unmaskFilename(b.output))
	}

	if b.error != "" {
		strBuilder.WriteString(" 2>")
		strBuilder.WriteString(unmaskFilename(b.error))
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

	if b.memPerNode == nil {
		if env, ok := os.LookupEnv("SBATCH_MEM_PER_NODE"); ok {
			val, err := resource.ParseQuantity(env)
			if err != nil {
				return fmt.Errorf("cannot parse '%s': %w", env, err)
			}
			b.memPerNode = ptr.To(val)
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

	if b.timeLimit == "" {
		b.timeLimit = os.Getenv("SBATCH_TIMELIMIT")
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

	if b.memPerNode == nil {
		b.memPerNode = scriptFlags.MemPerNode
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

	if b.timeLimit == "" {
		b.timeLimit = scriptFlags.TimeLimit
	}

	return nil
}

func newSlurmBuilder(b *Builder) *slurmBuilder {
	return &slurmBuilder{Builder: b}
}
