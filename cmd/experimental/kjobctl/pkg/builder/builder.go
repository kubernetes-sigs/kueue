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
	"slices"
	"time"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8s "k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/client-go/clientset/versioned"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/util"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
	kueueconstants "sigs.k8s.io/kueue/pkg/controller/constants"
)

var (
	noNamespaceSpecifiedErr                = errors.New("no namespace specified")
	noApplicationProfileSpecifiedErr       = errors.New("no application profile specified")
	noApplicationProfileModeSpecifiedErr   = errors.New("no application profile mode specified")
	invalidApplicationProfileModeErr       = errors.New("invalid application profile mode")
	applicationProfileModeNotConfiguredErr = errors.New("application profile mode not configured")
	noCommandSpecifiedErr                  = errors.New("no command specified")
	noParallelismSpecifiedErr              = errors.New("no parallelism specified")
	noCompletionsSpecifiedErr              = errors.New("no completions specified")
	noReplicasSpecifiedErr                 = errors.New("no replicas specified")
	noMinReplicasSpecifiedErr              = errors.New("no min-replicas specified")
	noMaxReplicasSpecifiedErr              = errors.New("no max-replicas specified")
	noRequestsSpecifiedErr                 = errors.New("no requests specified")
	noLocalQueueSpecifiedErr               = errors.New("no local queue specified")
	noRayClusterSpecifiedErr               = errors.New("no raycluster specified")
)

type builder interface {
	build(ctx context.Context) (runtime.Object, error)
}

type Builder struct {
	clientGetter     util.ClientGetter
	kjobctlClientset versioned.Interface
	k8sClientset     k8s.Interface

	namespace   string
	profileName string
	modeName    v1alpha1.ApplicationProfileMode

	command     []string
	parallelism *int32
	completions *int32
	replicas    map[string]int
	minReplicas map[string]int
	maxReplicas map[string]int
	requests    corev1.ResourceList
	localQueue  string
	rayCluster  string

	profile       *v1alpha1.ApplicationProfile
	mode          *v1alpha1.SupportedMode
	volumeBundles []v1alpha1.VolumeBundle

	buildTime time.Time
}

func NewBuilder(clientGetter util.ClientGetter, buildTime time.Time) *Builder {
	return &Builder{clientGetter: clientGetter, buildTime: buildTime}
}

func (b *Builder) WithNamespace(namespace string) *Builder {
	b.namespace = namespace
	return b
}

func (b *Builder) WithProfileName(profileName string) *Builder {
	b.profileName = profileName
	return b
}

func (b *Builder) WithModeName(modeName v1alpha1.ApplicationProfileMode) *Builder {
	b.modeName = modeName
	return b
}

func (b *Builder) WithCommand(command []string) *Builder {
	b.command = command
	return b
}

func (b *Builder) WithParallelism(parallelism *int32) *Builder {
	b.parallelism = parallelism
	return b
}

func (b *Builder) WithCompletions(completions *int32) *Builder {
	b.completions = completions
	return b
}

func (b *Builder) WithReplicas(replicas map[string]int) *Builder {
	b.replicas = replicas
	return b
}

func (b *Builder) WithMinReplicas(minReplicas map[string]int) *Builder {
	b.minReplicas = minReplicas
	return b
}

func (b *Builder) WithMaxReplicas(maxReplicas map[string]int) *Builder {
	b.maxReplicas = maxReplicas
	return b
}

func (b *Builder) WithRequests(requests corev1.ResourceList) *Builder {
	b.requests = requests
	return b
}

func (b *Builder) WithLocalQueue(localQueue string) *Builder {
	b.localQueue = localQueue
	return b
}

func (b *Builder) WithRayCluster(rayCluster string) *Builder {
	b.rayCluster = rayCluster
	return b
}

func (b *Builder) validateGeneral() error {
	if b.namespace == "" {
		return noNamespaceSpecifiedErr
	}

	if b.profileName == "" {
		return noApplicationProfileSpecifiedErr
	}

	if b.modeName == "" {
		return noApplicationProfileModeSpecifiedErr
	}

	return nil
}

func (b *Builder) complete(ctx context.Context) error {
	var err error

	b.kjobctlClientset, err = b.clientGetter.KjobctlClientset()
	if err != nil {
		return err
	}

	b.k8sClientset, err = b.clientGetter.K8sClientset()
	if err != nil {
		return err
	}

	b.profile, err = b.kjobctlClientset.KjobctlV1alpha1().ApplicationProfiles(b.namespace).Get(ctx, b.profileName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	for i, mode := range b.profile.Spec.SupportedModes {
		if mode.Name == b.modeName {
			b.mode = &b.profile.Spec.SupportedModes[i]
		}
	}

	if b.mode == nil {
		return applicationProfileModeNotConfiguredErr
	}

	volumeBundlesList, err := b.kjobctlClientset.KjobctlV1alpha1().VolumeBundles(b.profile.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	b.volumeBundles = volumeBundlesList.Items

	return nil
}

func (b *Builder) validateFlags() error {
	if slices.Contains(b.mode.RequiredFlags, v1alpha1.CmdFlag) && len(b.command) == 0 {
		return noCommandSpecifiedErr
	}

	if slices.Contains(b.mode.RequiredFlags, v1alpha1.ParallelismFlag) && b.parallelism == nil {
		return noParallelismSpecifiedErr
	}

	if slices.Contains(b.mode.RequiredFlags, v1alpha1.CompletionsFlag) && b.completions == nil {
		return noCompletionsSpecifiedErr
	}

	if slices.Contains(b.mode.RequiredFlags, v1alpha1.ReplicasFlag) && b.replicas == nil {
		return noReplicasSpecifiedErr
	}

	if slices.Contains(b.mode.RequiredFlags, v1alpha1.MinReplicasFlag) && b.minReplicas == nil {
		return noMinReplicasSpecifiedErr
	}

	if slices.Contains(b.mode.RequiredFlags, v1alpha1.MaxReplicasFlag) && b.maxReplicas == nil {
		return noMaxReplicasSpecifiedErr
	}

	if slices.Contains(b.mode.RequiredFlags, v1alpha1.RequestFlag) && b.requests == nil {
		return noRequestsSpecifiedErr
	}

	if slices.Contains(b.mode.RequiredFlags, v1alpha1.LocalQueueFlag) && b.localQueue == "" {
		return noLocalQueueSpecifiedErr
	}

	if slices.Contains(b.mode.RequiredFlags, v1alpha1.RayClusterFlag) && b.rayCluster == "" {
		return noRayClusterSpecifiedErr
	}

	return nil
}

func (b *Builder) Do(ctx context.Context) ([]runtime.Object, error) {
	if err := b.validateGeneral(); err != nil {
		return nil, err
	}

	var bImpl builder

	switch b.modeName {
	case v1alpha1.JobMode:
		bImpl = newJobBuilder(b)
	case v1alpha1.InteractiveMode:
		bImpl = newInteractiveBuilder(b)
	case v1alpha1.RayJobMode:
		bImpl = newRayJobBuilder(b)
	case v1alpha1.RayClusterMode:
		bImpl = newRayClusterBuilder(b)
	}

	if bImpl == nil {
		return nil, invalidApplicationProfileModeErr
	}

	if err := b.complete(ctx); err != nil {
		return nil, err
	}

	if err := b.validateFlags(); err != nil {
		return nil, err
	}

	objs := make([]runtime.Object, 0)
	o, err := bImpl.build(ctx)
	if err != nil {
		return nil, err
	}
	objs = append(objs, o)

	return objs, nil
}

func (b *Builder) buildObjectMeta(templateObjectMeta metav1.ObjectMeta) metav1.ObjectMeta {
	objectMeta := metav1.ObjectMeta{
		Namespace:    b.profile.Namespace,
		GenerateName: b.profile.Name + "-",
		Labels:       templateObjectMeta.Labels,
		Annotations:  templateObjectMeta.Annotations,
	}

	if objectMeta.Labels == nil {
		objectMeta.Labels = map[string]string{}
	}

	if b.profile != nil {
		objectMeta.Labels[constants.ProfileLabel] = b.profile.Name
	}

	if len(b.localQueue) > 0 {
		objectMeta.Labels[kueueconstants.QueueLabel] = b.localQueue
	}

	return objectMeta
}

func (b *Builder) buildPodSpec(templateSpec corev1.PodSpec) corev1.PodSpec {
	b.buildPodSpecVolumesAndEnv(&templateSpec)

	for i := range templateSpec.Containers {
		container := &templateSpec.Containers[i]

		if i == 0 && len(b.command) > 0 {
			container.Command = b.command
		}

		if i == 0 && len(b.requests) > 0 {
			container.Resources.Requests = b.requests
		}
	}

	return templateSpec
}

func (b *Builder) buildPodSpecVolumesAndEnv(templateSpec *corev1.PodSpec) {
	bundle := mergeBundles(b.volumeBundles)

	templateSpec.Volumes = append(templateSpec.Volumes, bundle.Spec.Volumes...)
	for i := range templateSpec.Containers {
		container := &templateSpec.Containers[i]

		container.VolumeMounts = append(container.VolumeMounts, bundle.Spec.ContainerVolumeMounts...)
		container.Env = append(container.Env, bundle.Spec.EnvVars...)
		container.Env = append(container.Env, b.additionalEnvironmentVariables()...)
	}

	for i := range templateSpec.InitContainers {
		initContainer := &templateSpec.InitContainers[i]

		initContainer.VolumeMounts = append(initContainer.VolumeMounts, bundle.Spec.ContainerVolumeMounts...)
		initContainer.Env = append(initContainer.Env, bundle.Spec.EnvVars...)
		initContainer.Env = append(initContainer.Env, b.additionalEnvironmentVariables()...)
	}
}

func (b *Builder) buildRayClusterSpec(spec *rayv1.RayClusterSpec) {
	b.buildPodSpecVolumesAndEnv(&spec.HeadGroupSpec.Template.Spec)

	for index := range spec.WorkerGroupSpecs {
		workerGroupSpec := &spec.WorkerGroupSpecs[index]

		if replicas, ok := b.replicas[workerGroupSpec.GroupName]; ok {
			workerGroupSpec.Replicas = ptr.To(int32(replicas))
		}
		if minReplicas, ok := b.minReplicas[workerGroupSpec.GroupName]; ok {
			workerGroupSpec.MinReplicas = ptr.To(int32(minReplicas))
		}
		if maxReplicas, ok := b.maxReplicas[workerGroupSpec.GroupName]; ok {
			workerGroupSpec.MaxReplicas = ptr.To(int32(maxReplicas))
		}

		b.buildPodSpecVolumesAndEnv(&workerGroupSpec.Template.Spec)
	}
}

func (b *Builder) additionalEnvironmentVariables() []corev1.EnvVar {
	userID := os.Getenv(constants.SystemEnvVarNameUser)
	timestamp := b.buildTime.Format(time.RFC3339)
	taskName := fmt.Sprintf("%s_%s", b.namespace, b.profileName)

	envVars := []corev1.EnvVar{
		{Name: constants.EnvVarNameUserID, Value: userID},
		{Name: constants.EnvVarTaskName, Value: taskName},
		{Name: constants.EnvVarTaskID, Value: fmt.Sprintf("%s_%s_%s", userID, timestamp, taskName)},
		{Name: constants.EnvVarNameProfile, Value: fmt.Sprintf("%s_%s", b.namespace, b.profileName)},
		{Name: constants.EnvVarNameTimestamp, Value: timestamp},
	}

	return envVars
}

func mergeBundles(bundles []v1alpha1.VolumeBundle) v1alpha1.VolumeBundle {
	var volumeBundle v1alpha1.VolumeBundle
	for _, b := range bundles {
		volumeBundle.Spec.Volumes = append(volumeBundle.Spec.Volumes, b.Spec.Volumes...)
		volumeBundle.Spec.ContainerVolumeMounts = append(volumeBundle.Spec.ContainerVolumeMounts, b.Spec.ContainerVolumeMounts...)
		volumeBundle.Spec.EnvVars = append(volumeBundle.Spec.EnvVars, b.Spec.EnvVars...)
	}

	return volumeBundle
}
