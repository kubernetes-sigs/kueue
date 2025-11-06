/*
Copyright 2024 The Kubeflow Authors.

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

package mpi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strconv"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation/field"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/kubeflow/trainer/v2/pkg/apply"
	"github.com/kubeflow/trainer/v2/pkg/constants"
	"github.com/kubeflow/trainer/v2/pkg/runtime"
	"github.com/kubeflow/trainer/v2/pkg/runtime/framework"
)

var (
	numProcPerNodePath = field.NewPath("spec").Child("trainer").Child("numProcPerNode")
)

// TODO : Support MPICH and IntelMPI implementations.

type MPI struct {
	client client.Client
	scheme *apiruntime.Scheme
}

var _ framework.CustomValidationPlugin = (*MPI)(nil)
var _ framework.EnforceMLPolicyPlugin = (*MPI)(nil)
var _ framework.WatchExtensionPlugin = (*MPI)(nil)
var _ framework.ComponentBuilderPlugin = (*MPI)(nil)

const Name = "MPI"

// +kubebuilder:rbac:groups="",resources=secrets,verbs=create;get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=create;get;list;watch;update;patch

func New(_ context.Context, client client.Client, _ client.FieldIndexer) (framework.Plugin, error) {
	return &MPI{
		client: client,
		scheme: client.Scheme(),
	}, nil
}

func (m *MPI) Name() string {
	return Name
}

// TODO (andreyvelich): Add validation to check that TrainJob doesn't have MPI envs.
// TODO (andreyvelich): We should validate that envs from different plugins don't conflict with each other.
// Ref: https://github.com/kubeflow/trainer/pull/2308#discussion_r1823229940
func (m *MPI) Validate(_ context.Context, runtimeInfo *runtime.Info, _, newJobObj *trainer.TrainJob) (admission.Warnings, field.ErrorList) {
	var allErrs field.ErrorList
	if runtimeInfo == nil || runtimeInfo.RuntimePolicy.MLPolicySource == nil || runtimeInfo.RuntimePolicy.MLPolicySource.MPI == nil {
		return nil, allErrs
	}
	specPath := field.NewPath("spec")
	if trainJobTrainer := newJobObj.Spec.Trainer; trainJobTrainer != nil && trainJobTrainer.NumProcPerNode != nil {
		if trainJobTrainer.NumProcPerNode.Type != intstr.Int {
			allErrs = append(allErrs, field.Invalid(numProcPerNodePath, *trainJobTrainer.NumProcPerNode, "must have an int value for MPI TrainJob"))
		}
	}
	// validate PodSet configurations based on NumNodes and RunLauncherAsNode.
	if trainJobTrainer := newJobObj.Spec.Trainer; trainJobTrainer != nil && ptr.Deref(trainJobTrainer.NumNodes, 1) >= 2 && ptr.Deref(runtimeInfo.RuntimePolicy.MLPolicySource.MPI.RunLauncherAsNode, false) {
		if runtimeInfo.FindPodSetByName(constants.Launcher) == nil || runtimeInfo.FindPodSetByName(constants.Node) == nil {
			numNodesPath := specPath.Child("trainer", "numNodes")
			allErrs = append(allErrs, field.Invalid(numNodesPath, newJobObj.Spec.Trainer.NumNodes, "must have 1 when MPI trainingRuntime with enabled runLauncherAsNode does not have either launcher and node"))
		}
	}
	return nil, allErrs
}

func (m *MPI) EnforceMLPolicy(info *runtime.Info, trainJob *trainer.TrainJob) error {
	if info == nil || info.RuntimePolicy.MLPolicySource == nil || info.RuntimePolicy.MLPolicySource.MPI == nil {
		return nil
	}

	// TrainJob contains the actual information for the Trainer.
	if trainJob.Spec.Trainer != nil && trainJob.Spec.Trainer.NumNodes != nil {
		if node := info.FindPodSetByName(constants.Node); node != nil && node.Count != nil {
			if ptr.Deref(info.RuntimePolicy.MLPolicySource.MPI.RunLauncherAsNode, false) {
				// When runLauncherAsNode is enabled, 1 nodes should be allocated to launcher.
				*node.Count = max(*trainJob.Spec.Trainer.NumNodes-1, 1)
			} else {
				*node.Count = *trainJob.Spec.Trainer.NumNodes
			}
		}
	}

	if trainJob.Spec.Trainer != nil && trainJob.Spec.Trainer.NumProcPerNode != nil {
		info.RuntimePolicy.MLPolicySource.MPI.NumProcPerNode = ptr.To(int32(trainJob.Spec.Trainer.NumProcPerNode.IntValue()))
		// If numProcPerNode is set to 1 in runtime, we make it equal to number of GPUs.
	} else if *info.RuntimePolicy.MLPolicySource.MPI.NumProcPerNode == 1 {
		resourcesPerNode := ptr.Deref(runtime.ExtractResourcePerNodeFromRuntime(info), corev1.ResourceRequirements{})
		if jobTrainer := trainJob.Spec.Trainer; jobTrainer != nil && jobTrainer.ResourcesPerNode != nil {
			resourcesPerNode = ptr.Deref(jobTrainer.ResourcesPerNode, corev1.ResourceRequirements{})
		}
		if gpuQ := runtime.GetNumGPUPerNode(&resourcesPerNode); gpuQ > 1 {
			info.RuntimePolicy.MLPolicySource.MPI.NumProcPerNode = ptr.To(int32(gpuQ))
		}
	}

	// Add Secret and ConfigMap volumes to the Info object
	for psIdx, ps := range info.TemplateSpec.PodSets {
		if ps.Name != constants.Node && ps.Name != constants.Launcher {
			continue
		}
		apply.UpsertVolumes(
			&info.TemplateSpec.PodSets[psIdx].Volumes,
			[]corev1ac.VolumeApplyConfiguration{
				*corev1ac.Volume().
					WithName(constants.MPISSHAuthVolumeName).
					WithSecret(corev1ac.SecretVolumeSource().
						WithSecretName(fmt.Sprintf("%s%s", trainJob.Name, constants.MPISSHAuthSecretSuffix)).
						WithItems(
							corev1ac.KeyToPath().
								WithKey(corev1.SSHAuthPrivateKey).
								WithPath(constants.MPISSHPrivateKeyFile),
							corev1ac.KeyToPath().
								WithKey(constants.MPISSHPublicKey).
								WithPath(constants.MPISSHPublicKeyFile),
							corev1ac.KeyToPath().
								WithKey(constants.MPISSHPublicKey).
								WithPath(constants.MPISSHAuthorizedKeys),
						),
					),
			}...,
		)
		if ps.Name == constants.Launcher {
			apply.UpsertVolumes(
				&info.TemplateSpec.PodSets[psIdx].Volumes,
				[]corev1ac.VolumeApplyConfiguration{
					*corev1ac.Volume().
						WithName(constants.MPIHostfileVolumeName).
						WithConfigMap(corev1ac.ConfigMapVolumeSource().
							WithName(fmt.Sprintf("%s%s", trainJob.Name, constants.MPIHostfileConfigMapSuffix)).
							WithItems(
								corev1ac.KeyToPath().
									WithKey(constants.MPIHostfileName).
									WithPath(constants.MPIHostfileName).
									WithMode(0444),
							),
						),
				}...,
			)
		}
		for cIdx, container := range ps.Containers {
			if container.Name != constants.Node {
				continue
			}
			apply.UpsertVolumeMounts(
				&info.TemplateSpec.PodSets[psIdx].Containers[cIdx].VolumeMounts,
				[]corev1ac.VolumeMountApplyConfiguration{
					*corev1ac.VolumeMount().
						WithName(constants.MPISSHAuthVolumeName).
						WithMountPath(*info.RuntimePolicy.MLPolicySource.MPI.SSHAuthMountPath),
				}...,
			)
			if ps.Name == constants.Launcher && (container.Name == constants.Node || container.Name == constants.Launcher) {
				apply.UpsertVolumeMounts(
					&info.TemplateSpec.PodSets[psIdx].Containers[cIdx].VolumeMounts,
					*corev1ac.VolumeMount().
						WithName(constants.MPIHostfileVolumeName).
						WithMountPath(constants.MPIHostfileDir),
				)
				switch *info.RuntimePolicy.MLPolicySource.MPI.MPIImplementation {
				case trainer.MPIImplementationOpenMPI:
					apply.UpsertEnvVars(
						&info.TemplateSpec.PodSets[psIdx].Containers[cIdx].Env,
						*corev1ac.EnvVar().
							WithName(constants.OpenMPIEnvHostFileLocation).
							WithValue(fmt.Sprintf("%s/%s", constants.MPIHostfileDir, constants.MPIHostfileName)),
						*corev1ac.EnvVar().
							WithName(constants.OpenMPIEnvKeepFQDNHostNames).
							WithValue("true"),
						*corev1ac.EnvVar().
							WithName(constants.OpenMPIEnvDefaultSlots).
							WithValue(strconv.Itoa(int(*info.RuntimePolicy.MLPolicySource.MPI.NumProcPerNode))),
						*corev1ac.EnvVar().
							WithName(constants.OpenMPIEnvKeyRSHArgs).
							WithValue(constants.OpenMPIEnvDefaultValueRSHArgs),
					)
				default:
					return fmt.Errorf("MPI implementation for %v doesn't supported", info.RuntimePolicy.MLPolicySource.MPI.MPIImplementation)
				}
			}
		}
	}
	info.SyncPodSetsToTemplateSpec()
	return nil
}

func (m *MPI) ReconcilerBuilders() []runtime.ReconcilerBuilder {
	return []runtime.ReconcilerBuilder{
		func(b *builder.Builder, cl client.Client, cache cache.Cache) *builder.Builder {
			return b.Watches(
				&corev1.ConfigMap{},
				handler.EnqueueRequestForOwner(
					m.client.Scheme(), m.client.RESTMapper(), &trainer.TrainJob{}, handler.OnlyControllerOwner(),
				),
			)
		},
		func(b *builder.Builder, cl client.Client, cache cache.Cache) *builder.Builder {
			return b.Watches(
				&corev1.Secret{},
				handler.EnqueueRequestForOwner(
					m.client.Scheme(), m.client.RESTMapper(), &trainer.TrainJob{}, handler.OnlyControllerOwner(),
				),
			)
		},
	}
}

func (m *MPI) Build(ctx context.Context, info *runtime.Info, trainJob *trainer.TrainJob) ([]any, error) {
	if info == nil || info.RuntimePolicy.MLPolicySource == nil || info.RuntimePolicy.MLPolicySource.MPI == nil {
		return nil, nil
	}

	var objects []any

	// SSHAuthSecret is immutable.
	if err := m.client.Get(ctx, client.ObjectKey{Name: sshAuthSecretName(trainJob.Name), Namespace: trainJob.Namespace}, &corev1.Secret{}); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return nil, err
		}
		secret, err := m.buildSSHAuthSecret(trainJob)
		if err != nil {
			return nil, fmt.Errorf("failed to build SSH Auth secret: %w", err)
		}
		objects = append(objects, secret)
	}
	return append(objects, m.buildHostFileConfigMap(info, trainJob)), nil
}

func (m *MPI) buildSSHAuthSecret(trainJob *trainer.TrainJob) (*corev1ac.SecretApplyConfiguration, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	if err != nil {
		return nil, err
	}
	privateDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: privateDER,
	})
	publicKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, err
	}
	return corev1ac.Secret(sshAuthSecretName(trainJob.Name), trainJob.Namespace).
		WithType(corev1.SecretTypeSSHAuth).
		WithData(map[string][]byte{
			corev1.SSHAuthPrivateKey:  privatePEM,
			constants.MPISSHPublicKey: ssh.MarshalAuthorizedKey(publicKey),
		}).
		WithImmutable(true).
		WithOwnerReferences(metav1ac.OwnerReference().
			WithAPIVersion(trainer.GroupVersion.String()).
			WithKind(trainer.TrainJobKind).
			WithName(trainJob.Name).
			WithUID(trainJob.UID).
			WithController(true).
			WithBlockOwnerDeletion(true)), nil
}

func sshAuthSecretName(trainJobName string) string {
	return fmt.Sprintf("%s%s", trainJobName, constants.MPISSHAuthSecretSuffix)
}

func (m *MPI) buildHostFileConfigMap(info *runtime.Info, trainJob *trainer.TrainJob) *corev1ac.ConfigMapApplyConfiguration {
	var hostFile bytes.Buffer
	runLauncherAsNode := ptr.Deref(info.RuntimePolicy.MLPolicySource.MPI.RunLauncherAsNode, false)
	slots := ptr.Deref(info.RuntimePolicy.MLPolicySource.MPI.NumProcPerNode, 1)
	for _, ps := range info.TemplateSpec.PodSets {
		if !isNode(runLauncherAsNode, ps) {
			continue
		}
		switch *info.RuntimePolicy.MLPolicySource.MPI.MPIImplementation {
		case trainer.MPIImplementationOpenMPI:
			for e := range ps.Endpoints {
				hostFile.WriteString(fmt.Sprintf("%s slots=%d\n", e, slots))
			}
		}
	}
	return corev1ac.ConfigMap(fmt.Sprintf("%s%s", trainJob.Name, constants.MPIHostfileConfigMapSuffix), trainJob.Namespace).
		WithData(map[string]string{
			constants.MPIHostfileName: hostFile.String(),
		}).
		WithOwnerReferences(metav1ac.OwnerReference().
			WithAPIVersion(trainer.GroupVersion.String()).
			WithKind(trainer.TrainJobKind).
			WithName(trainJob.Name).
			WithUID(trainJob.UID).
			WithController(true).
			WithBlockOwnerDeletion(true))
}

func isNode(runLauncherAsNode bool, ps runtime.PodSet) bool {
	return (runLauncherAsNode && ps.Name == constants.Launcher) || ps.Name == constants.Node
}
