package utils

import (
	errstd "errors"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"

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

// Validation for invalid Ray Cluster configurations.
func ValidateRayClusterSpec(instance *rayv1.RayCluster) error {
	if len(instance.Spec.HeadGroupSpec.Template.Spec.Containers) == 0 {
		return fmt.Errorf("headGroupSpec should have at least one container")
	}

	for _, workerGroup := range instance.Spec.WorkerGroupSpecs {
		if len(workerGroup.Template.Spec.Containers) == 0 {
			return fmt.Errorf("workerGroupSpec should have at least one container")
		}
	}

	if instance.Annotations[RayFTEnabledAnnotationKey] != "" && instance.Spec.GcsFaultToleranceOptions != nil {
		return fmt.Errorf("%s annotation and GcsFaultToleranceOptions are both set. "+
			"Please use only GcsFaultToleranceOptions to configure GCS fault tolerance", RayFTEnabledAnnotationKey)
	}

	if !IsGCSFaultToleranceEnabled(*instance) {
		if EnvVarExists(RAY_REDIS_ADDRESS, instance.Spec.HeadGroupSpec.Template.Spec.Containers[RayContainerIndex].Env) {
			return fmt.Errorf("%s is set which implicitly enables GCS fault tolerance, "+
				"but GcsFaultToleranceOptions is not set. Please set GcsFaultToleranceOptions "+
				"to enable GCS fault tolerance", RAY_REDIS_ADDRESS)
		}
	}

	headContainer := instance.Spec.HeadGroupSpec.Template.Spec.Containers[RayContainerIndex]
	if instance.Spec.GcsFaultToleranceOptions != nil {
		if redisPassword := instance.Spec.HeadGroupSpec.RayStartParams["redis-password"]; redisPassword != "" {
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

		if instance.Annotations[RayExternalStorageNSAnnotationKey] != "" {
			return fmt.Errorf("cannot set `ray.io/external-storage-namespace` annotation when " +
				"GcsFaultToleranceOptions is enabled - use GcsFaultToleranceOptions.ExternalStorageNamespace instead")
		}
	}
	if instance.Spec.HeadGroupSpec.RayStartParams["redis-username"] != "" || EnvVarExists(REDIS_USERNAME, headContainer.Env) {
		return fmt.Errorf("cannot set redis username in rayStartParams or environment variables" +
			" - use GcsFaultToleranceOptions.RedisUsername instead")
	}

	if !features.Enabled(features.RayJobDeletionPolicy) {
		for _, workerGroup := range instance.Spec.WorkerGroupSpecs {
			if workerGroup.Suspend != nil && *workerGroup.Suspend {
				return fmt.Errorf("suspending worker groups is currently available when the RayJobDeletionPolicy feature gate is enabled")
			}
		}
	}

	if IsAutoscalingEnabled(instance) {
		for _, workerGroup := range instance.Spec.WorkerGroupSpecs {
			if workerGroup.Suspend != nil && *workerGroup.Suspend {
				// TODO (rueian): This can be supported in future Ray. We should check the RayVersion once we know the version.
				return fmt.Errorf("suspending worker groups is not currently supported with Autoscaler enabled")
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

func ValidateRayJobSpec(rayJob *rayv1.RayJob) error {
	// KubeRay has some limitations for the suspend operation. The limitations are a subset of the limitations of
	// Kueue (https://kueue.sigs.k8s.io/docs/tasks/run_rayjobs/#c-limitations). For example, KubeRay allows users
	// to suspend a RayJob with autoscaling enabled, but Kueue doesn't.
	if rayJob.Spec.Suspend && !rayJob.Spec.ShutdownAfterJobFinishes {
		return fmt.Errorf("a RayJob with shutdownAfterJobFinishes set to false is not allowed to be suspended")
	}

	isClusterSelectorMode := len(rayJob.Spec.ClusterSelector) != 0
	if rayJob.Spec.Suspend && isClusterSelectorMode {
		return fmt.Errorf("the ClusterSelector mode doesn't support the suspend operation")
	}
	if rayJob.Spec.RayClusterSpec == nil && !isClusterSelectorMode {
		return fmt.Errorf("one of RayClusterSpec or ClusterSelector must be set")
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

		if policy == rayv1.DeleteWorkersDeletionPolicy && IsAutoscalingEnabled(rayJob) {
			// TODO (rueian): This can be supported in a future Ray version. We should check the RayVersion once we know it.
			return fmt.Errorf("DeletionPolicy=DeleteWorkers currently does not support RayCluster with autoscaling enabled")
		}

		if rayJob.Spec.ShutdownAfterJobFinishes && policy == rayv1.DeleteNoneDeletionPolicy {
			return fmt.Errorf("shutdownAfterJobFinshes is set to 'true' while deletion policy is 'DeleteNone'")
		}
	}
	return nil
}

func ValidateRayServiceSpec(rayService *rayv1.RayService) error {
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
