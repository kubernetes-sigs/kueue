package common

import (
	"encoding/json"
	"fmt"
	"strings"

	semver "github.com/Masterminds/semver/v3"
	"github.com/google/shlex"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
)

// GetRuntimeEnvJson returns the JSON string of the runtime environment for the Ray job.
func getRuntimeEnvJson(rayJobInstance *rayv1.RayJob) (string, error) {
	runtimeEnvYAML := rayJobInstance.Spec.RuntimeEnvYAML

	if len(runtimeEnvYAML) > 0 {
		// Convert YAML to JSON
		jsonData, err := yaml.YAMLToJSON([]byte(runtimeEnvYAML))
		if err != nil {
			return "", err
		}
		// We return the JSON as a string
		return string(jsonData), nil
	}

	return "", nil
}

// GetBaseRayJobCommand returns the first part of the Ray Job command up to and including the address, e.g. "ray job submit --address http://..."
func GetBaseRayJobCommand(address string) []string {
	// add http:// if needed
	if !strings.HasPrefix(address, "http://") {
		address = "http://" + address
	}
	return []string{"ray", "job", "submit", "--address", address}
}

// GetMetadataJson returns the JSON string of the metadata for the Ray job.
func GetMetadataJson(metadata map[string]string, rayVersion string) (string, error) {
	// Check that the Ray version is at least 2.6.0.
	// If it is, we can use the --metadata-json flag.
	// Otherwise, we need to raise an error.
	constraint, _ := semver.NewConstraint(">= 2.6.0")
	v, err := semver.NewVersion(rayVersion)
	if err != nil {
		return "", fmt.Errorf("failed to parse Ray version: %v: %w", rayVersion, err)
	}
	if !constraint.Check(v) {
		return "", fmt.Errorf("the Ray version must be at least 2.6.0 to use the metadata field")
	}
	// Convert the metadata map to a JSON string.
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("failed to marshal metadata: %v: %w", metadata, err)
	}
	return string(metadataBytes), nil
}

// GetK8sJobCommand builds the K8s job command for the Ray job.
func GetK8sJobCommand(rayJobInstance *rayv1.RayJob) ([]string, error) {
	address := rayJobInstance.Status.DashboardURL
	metadata := rayJobInstance.Spec.Metadata
	jobId := rayJobInstance.Status.JobId
	entrypoint := rayJobInstance.Spec.Entrypoint
	entrypointNumCpus := rayJobInstance.Spec.EntrypointNumCpus
	entrypointNumGpus := rayJobInstance.Spec.EntrypointNumGpus
	entrypointResources := rayJobInstance.Spec.EntrypointResources

	k8sJobCommand := GetBaseRayJobCommand(address)

	runtimeEnvJson, err := getRuntimeEnvJson(rayJobInstance)
	if err != nil {
		return nil, err
	}
	if len(runtimeEnvJson) > 0 {
		k8sJobCommand = append(k8sJobCommand, "--runtime-env-json", runtimeEnvJson)
	}

	if len(metadata) > 0 {
		metadataJson, err := GetMetadataJson(metadata, rayJobInstance.Spec.RayClusterSpec.RayVersion)
		if err != nil {
			return nil, err
		}
		k8sJobCommand = append(k8sJobCommand, "--metadata-json", metadataJson)
	}

	if len(jobId) > 0 {
		k8sJobCommand = append(k8sJobCommand, "--submission-id", jobId)
	}

	if entrypointNumCpus > 0 {
		k8sJobCommand = append(k8sJobCommand, "--entrypoint-num-cpus", fmt.Sprintf("%f", entrypointNumCpus))
	}

	if entrypointNumGpus > 0 {
		k8sJobCommand = append(k8sJobCommand, "--entrypoint-num-gpus", fmt.Sprintf("%f", entrypointNumGpus))
	}

	if len(entrypointResources) > 0 {
		k8sJobCommand = append(k8sJobCommand, "--entrypoint-resources", entrypointResources)
	}

	// "--" is used to separate the entrypoint from the Ray Job CLI command and its arguments.
	k8sJobCommand = append(k8sJobCommand, "--")

	commandSlice, err := shlex.Split(entrypoint)
	if err != nil {
		return nil, err
	}
	k8sJobCommand = append(k8sJobCommand, commandSlice...)

	return k8sJobCommand, nil
}

// GetDefaultSubmitterTemplate creates a default submitter template for the Ray job.
func GetDefaultSubmitterTemplate(rayClusterInstance *rayv1.RayCluster) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "ray-job-submitter",
					// Use the image of the Ray head to be defensive against version mismatch issues
					Image: rayClusterInstance.Spec.HeadGroupSpec.Template.Spec.Containers[utils.RayContainerIndex].Image,
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("200Mi"),
						},
					},
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
}
