package utils

import (
	errstd "errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"github.com/ray-project/kuberay/ray-operator/pkg/features"
)

func ValidateRayClusterStatus(instance *rayv1.RayCluster) error {
	suspending := meta.IsStatusConditionTrue(instance.Status.Conditions, string(rayv1.RayClusterSuspending))
	suspended := meta.IsStatusConditionTrue(instance.Status.Conditions, string(rayv1.RayClusterSuspended))
	if suspending && suspended {
		return errstd.New("invalid RayCluster State: rayv1.RayClusterSuspending and rayv1.RayClusterSuspended conditions should not be both true")
	}
	return nil
}

func ValidateRayClusterMetadata(metadata metav1.ObjectMeta) error {
	if len(metadata.Name) > MaxRayClusterNameLength {
		return fmt.Errorf("RayCluster name should be no more than %d characters", MaxRayClusterNameLength)
	}
	if errs := validation.IsDNS1035Label(metadata.Name); len(errs) > 0 {
		return fmt.Errorf("RayCluster name should be a valid DNS1035 label: %v", errs)
	}
	return nil
}

// Validation for invalid Ray Cluster configurations.
func ValidateRayClusterSpec(spec *rayv1.RayClusterSpec, annotations map[string]string) error {
	if len(spec.HeadGroupSpec.Template.Spec.Containers) == 0 {
		return fmt.Errorf("headGroupSpec should have at least one container")
	}

	for _, workerGroup := range spec.WorkerGroupSpecs {
		if len(workerGroup.Template.Spec.Containers) == 0 {
			return fmt.Errorf("workerGroupSpec should have at least one container")
		}
	}

	if annotations[RayFTEnabledAnnotationKey] != "" && spec.GcsFaultToleranceOptions != nil {
		return fmt.Errorf("%s annotation and GcsFaultToleranceOptions are both set. "+
			"Please use only GcsFaultToleranceOptions to configure GCS fault tolerance", RayFTEnabledAnnotationKey)
	}

	if !IsGCSFaultToleranceEnabled(spec, annotations) {
		if EnvVarExists(RAY_REDIS_ADDRESS, spec.HeadGroupSpec.Template.Spec.Containers[RayContainerIndex].Env) {
			return fmt.Errorf("%s is set which implicitly enables GCS fault tolerance, "+
				"but GcsFaultToleranceOptions is not set. Please set GcsFaultToleranceOptions "+
				"to enable GCS fault tolerance", RAY_REDIS_ADDRESS)
		}
	}

	headContainer := spec.HeadGroupSpec.Template.Spec.Containers[RayContainerIndex]
	if spec.GcsFaultToleranceOptions != nil {
		if redisPassword := spec.HeadGroupSpec.RayStartParams["redis-password"]; redisPassword != "" {
			return fmt.Errorf("cannot set `redis-password` in rayStartParams when " +
				"GcsFaultToleranceOptions is enabled - use GcsFaultToleranceOptions.RedisPassword instead")
		}

		if EnvVarExists(REDIS_PASSWORD, headContainer.Env) {
			return fmt.Errorf("cannot set `REDIS_PASSWORD` env var in head Pod when " +
				"GcsFaultToleranceOptions is enabled - use GcsFaultToleranceOptions.RedisPassword instead")
		}

		if EnvVarExists(RAY_REDIS_ADDRESS, headContainer.Env) {
			return fmt.Errorf("cannot set `RAY_REDIS_ADDRESS` env var in head Pod when " +
				"GcsFaultToleranceOptions is enabled - use GcsFaultToleranceOptions.RedisAddress instead")
		}

		if annotations[RayExternalStorageNSAnnotationKey] != "" {
			return fmt.Errorf("cannot set `ray.io/external-storage-namespace` annotation when " +
				"GcsFaultToleranceOptions is enabled - use GcsFaultToleranceOptions.ExternalStorageNamespace instead")
		}
	}
	if spec.HeadGroupSpec.RayStartParams["redis-username"] != "" || EnvVarExists(REDIS_USERNAME, headContainer.Env) {
		return fmt.Errorf("cannot set redis username in rayStartParams or environment variables" +
			" - use GcsFaultToleranceOptions.RedisUsername instead")
	}

	if !features.Enabled(features.RayJobDeletionPolicy) {
		for _, workerGroup := range spec.WorkerGroupSpecs {
			if workerGroup.Suspend != nil && *workerGroup.Suspend {
				return fmt.Errorf("worker group %s can be suspended only when the RayJobDeletionPolicy feature gate is enabled", workerGroup.GroupName)
			}
		}
	}

	if IsAutoscalingEnabled(spec) {
		for _, workerGroup := range spec.WorkerGroupSpecs {
			if workerGroup.Suspend != nil && *workerGroup.Suspend {
				// TODO (rueian): This can be supported in future Ray. We should check the RayVersion once we know the version.
				return fmt.Errorf("worker group %s cannot be suspended with Autoscaler enabled", workerGroup.GroupName)
			}
		}

		if spec.AutoscalerOptions != nil && spec.AutoscalerOptions.Version != nil && EnvVarExists(RAY_ENABLE_AUTOSCALER_V2, spec.HeadGroupSpec.Template.Spec.Containers[RayContainerIndex].Env) {
			return fmt.Errorf("both .spec.autoscalerOptions.version and head Pod env var %s are set, please only use the former", RAY_ENABLE_AUTOSCALER_V2)
		}

		if IsAutoscalingV2Enabled(spec) {
			if spec.HeadGroupSpec.Template.Spec.RestartPolicy != "" && spec.HeadGroupSpec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
				return fmt.Errorf("restartPolicy for head Pod should be Never or unset when using autoscaler V2")
			}

			for _, workerGroup := range spec.WorkerGroupSpecs {
				if workerGroup.Template.Spec.RestartPolicy != "" && workerGroup.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
					return fmt.Errorf("restartPolicy for worker group %s should be Never or unset when using autoscaler V2", workerGroup.GroupName)
				}
			}
		}
	}
	return nil
}

func ValidateRayJobStatus(rayJob *rayv1.RayJob) error {
	if rayJob.Status.JobDeploymentStatus == rayv1.JobDeploymentStatusWaiting && rayJob.Spec.SubmissionMode != rayv1.InteractiveMode {
		return fmt.Errorf("invalid RayJob State: JobDeploymentStatus cannot be `Waiting` when SubmissionMode is not InteractiveMode")
	}
	return nil
}

func ValidateRayJobMetadata(metadata metav1.ObjectMeta) error {
	if len(metadata.Name) > MaxRayJobNameLength {
		return fmt.Errorf("RayJob name should be no more than %d characters", MaxRayJobNameLength)
	}
	if errs := validation.IsDNS1035Label(metadata.Name); len(errs) > 0 {
		return fmt.Errorf("RayJob name should be a valid DNS1035 label: %v", errs)
	}
	return nil
}

func ValidateRayJobSpec(rayJob *rayv1.RayJob) error {
	// KubeRay has some limitations for the suspend operation. The limitations are a subset of the limitations of
	// Kueue (https://kueue.sigs.k8s.io/docs/tasks/run_rayjobs/#c-limitations). For example, KubeRay allows users
	// to suspend a RayJob with autoscaling enabled, but Kueue doesn't.
	if rayJob.Spec.Suspend && !rayJob.Spec.ShutdownAfterJobFinishes {
		return fmt.Errorf("a RayJob with shutdownAfterJobFinishes set to false is not allowed to be suspended")
	}

	if rayJob.Spec.TTLSecondsAfterFinished < 0 {
		return fmt.Errorf("TTLSecondsAfterFinished must be a non-negative integer")
	}

	if !rayJob.Spec.ShutdownAfterJobFinishes && rayJob.Spec.TTLSecondsAfterFinished > 0 {
		return fmt.Errorf("a RayJob with shutdownAfterJobFinishes set to false cannot have TTLSecondsAfterFinished")
	}

	isClusterSelectorMode := len(rayJob.Spec.ClusterSelector) != 0
	if rayJob.Spec.Suspend && isClusterSelectorMode {
		return fmt.Errorf("the ClusterSelector mode doesn't support the suspend operation")
	}
	if rayJob.Spec.RayClusterSpec == nil && !isClusterSelectorMode {
		return fmt.Errorf("one of RayClusterSpec or ClusterSelector must be set")
	}
	// InteractiveMode does not support backoffLimit > 1.
	// When a RayJob fails (e.g., due to a missing script) and retries,
	// spec.JobId remains set, causing the new job to incorrectly transition
	// to Running instead of Waiting or Failed.
	// After discussion, we decided to disallow retries in InteractiveMode
	// to avoid ambiguous state handling and unintended behavior.
	// https://github.com/ray-project/kuberay/issues/3525
	if rayJob.Spec.SubmissionMode == rayv1.InteractiveMode && rayJob.Spec.BackoffLimit != nil && *rayJob.Spec.BackoffLimit > 0 {
		return fmt.Errorf("BackoffLimit is incompatible with InteractiveMode")
	}

	if rayJob.Spec.RayClusterSpec != nil {
		if err := ValidateRayClusterSpec(rayJob.Spec.RayClusterSpec, rayJob.Annotations); err != nil {
			return err
		}
	}

	// Validate whether RuntimeEnvYAML is a valid YAML string. Note that this only checks its validity
	// as a YAML string, not its adherence to the runtime environment schema.
	if _, err := UnmarshalRuntimeEnvYAML(rayJob.Spec.RuntimeEnvYAML); err != nil {
		return err
	}
	if rayJob.Spec.ActiveDeadlineSeconds != nil && *rayJob.Spec.ActiveDeadlineSeconds <= 0 {
		return fmt.Errorf("activeDeadlineSeconds must be a positive integer")
	}
	if rayJob.Spec.BackoffLimit != nil && *rayJob.Spec.BackoffLimit < 0 {
		return fmt.Errorf("backoffLimit must be a positive integer")
	}
	if !features.Enabled(features.RayJobDeletionPolicy) && rayJob.Spec.DeletionPolicy != nil {
		return fmt.Errorf("RayJobDeletionPolicy feature gate must be enabled to use the DeletionPolicy feature")
	}

	if rayJob.Spec.DeletionPolicy != nil {
		policy := *rayJob.Spec.DeletionPolicy
		if isClusterSelectorMode {
			switch policy {
			case rayv1.DeleteClusterDeletionPolicy:
				return fmt.Errorf("the ClusterSelector mode doesn't support DeletionPolicy=DeleteCluster")
			case rayv1.DeleteWorkersDeletionPolicy:
				return fmt.Errorf("the ClusterSelector mode doesn't support DeletionPolicy=DeleteWorkers")
			}
		}

		if policy == rayv1.DeleteWorkersDeletionPolicy && IsAutoscalingEnabled(rayJob.Spec.RayClusterSpec) {
			// TODO (rueian): This can be supported in a future Ray version. We should check the RayVersion once we know it.
			return fmt.Errorf("DeletionPolicy=DeleteWorkers currently does not support RayCluster with autoscaling enabled")
		}

		if rayJob.Spec.ShutdownAfterJobFinishes && policy == rayv1.DeleteNoneDeletionPolicy {
			return fmt.Errorf("shutdownAfterJobFinshes is set to 'true' while deletion policy is 'DeleteNone'")
		}
	}
	return nil
}

func ValidateRayServiceMetadata(metadata metav1.ObjectMeta) error {
	if len(metadata.Name) > MaxRayServiceNameLength {
		return fmt.Errorf("RayService name should be no more than %d characters", MaxRayServiceNameLength)
	}
	if errs := validation.IsDNS1035Label(metadata.Name); len(errs) > 0 {
		return fmt.Errorf("RayService name should be a valid DNS1035 label: %v", errs)
	}
	return nil
}

func ValidateRayServiceSpec(rayService *rayv1.RayService) error {
	if err := ValidateRayClusterSpec(&rayService.Spec.RayClusterSpec, rayService.Annotations); err != nil {
		return err
	}

	if headSvc := rayService.Spec.RayClusterSpec.HeadGroupSpec.HeadService; headSvc != nil && headSvc.Name != "" {
		return fmt.Errorf("spec.rayClusterConfig.headGroupSpec.headService.metadata.name should not be set")
	}

	// only NewCluster and None are valid upgradeType
	if rayService.Spec.UpgradeStrategy != nil &&
		rayService.Spec.UpgradeStrategy.Type != nil &&
		*rayService.Spec.UpgradeStrategy.Type != rayv1.None &&
		*rayService.Spec.UpgradeStrategy.Type != rayv1.NewCluster {
		return fmt.Errorf("Spec.UpgradeStrategy.Type value %s is invalid, valid options are %s or %s", *rayService.Spec.UpgradeStrategy.Type, rayv1.NewCluster, rayv1.None)
	}
	return nil
}
