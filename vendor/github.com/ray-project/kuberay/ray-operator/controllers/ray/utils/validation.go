package utils

import (
	errstd "errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/version"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"github.com/ray-project/kuberay/ray-operator/controllers/ray/utils/dashboardclient"
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

func ValidateRayClusterUpgradeOptions(instance *rayv1.RayCluster) error {
	if instance.Spec.UpgradeStrategy != nil && instance.Spec.UpgradeStrategy.Type != nil &&
		*instance.Spec.UpgradeStrategy.Type != rayv1.RayClusterRecreate &&
		*instance.Spec.UpgradeStrategy.Type != rayv1.RayClusterUpgradeNone {
		return fmt.Errorf("The RayCluster spec is invalid: Spec.UpgradeStrategy.Type value %s is invalid, valid options are %s or %s", *instance.Spec.UpgradeStrategy.Type, rayv1.RayClusterRecreate, rayv1.RayClusterUpgradeNone)
	}

	// only allow UpgradeStrategy to be set when RayCluster is created directly by user
	if instance.Spec.UpgradeStrategy != nil && instance.Spec.UpgradeStrategy.Type != nil {
		creatorCRDType := GetCRDType(instance.Labels[RayOriginatedFromCRDLabelKey])
		if creatorCRDType == RayJobCRD || creatorCRDType == RayServiceCRD {
			return fmt.Errorf("upgradeStrategy cannot be set when RayCluster is created by %s", creatorCRDType)
		}
	}
	return nil
}

// validateRayGroupResources checks for conflicting resource definitions.
func validateRayGroupResources(groupName string, rayStartParams, resources map[string]string) error {
	hasRayStartResources := rayStartParams["num-cpus"] != "" ||
		rayStartParams["num-gpus"] != "" ||
		rayStartParams["memory"] != "" ||
		rayStartParams["resources"] != ""
	if hasRayStartResources && len(resources) > 0 {
		return fmt.Errorf("resource fields should not be set in both rayStartParams and Resources for %s group; please use only one", groupName)
	}
	return nil
}

// validateRayGroupLabels checks for invalid label definitions and correct label syntax.
func validateRayGroupLabels(groupName string, rayStartParams, labels map[string]string) error {
	if _, ok := rayStartParams["labels"]; ok {
		return fmt.Errorf("rayStartParams['labels'] is not supported for %s group; please use the top-level Labels field instead", groupName)
	}

	// Validate that labels conforms to Kubernetes label syntax.
	var allErrs []string
	for key, val := range labels {
		// Validate the label key.
		if errs := validation.IsQualifiedName(key); len(errs) > 0 {
			for _, err := range errs {
				allErrs = append(allErrs, fmt.Sprintf("invalid label key for %s group: '%s', error: %s", groupName, key, err))
			}
		}

		// Validate the label value.
		if errs := validation.IsValidLabelValue(val); len(errs) > 0 {
			for _, err := range errs {
				allErrs = append(allErrs, fmt.Sprintf("invalid label value for key '%s' in %s group: '%s', error: %s", key, groupName, val, err))
			}
		}
	}

	if len(allErrs) > 0 {
		return fmt.Errorf("%s", strings.Join(allErrs, "; "))
	}

	return nil
}

// Validation for invalid Ray Cluster configurations.
func ValidateRayClusterSpec(spec *rayv1.RayClusterSpec, annotations map[string]string) error {
	if len(spec.HeadGroupSpec.Template.Spec.Containers) == 0 {
		return fmt.Errorf("headGroupSpec should have at least one container")
	}

	if err := validateRayGroupResources("Head", spec.HeadGroupSpec.RayStartParams, spec.HeadGroupSpec.Resources); err != nil {
		return err
	}
	if err := validateRayGroupLabels("Head", spec.HeadGroupSpec.RayStartParams, spec.HeadGroupSpec.Labels); err != nil {
		return err
	}

	// Check if autoscaling is enabled once to avoid repeated calls
	isAutoscalingEnabled := IsAutoscalingEnabled(spec)

	for _, workerGroup := range spec.WorkerGroupSpecs {
		if len(workerGroup.Template.Spec.Containers) == 0 {
			return fmt.Errorf("workerGroupSpec should have at least one container")
		}

		// When autoscaling is enabled, MinReplicas and MaxReplicas are optional
		// as users can manually update them and the autoscaler will handle the adjustment.
		if !isAutoscalingEnabled && (workerGroup.MinReplicas == nil || workerGroup.MaxReplicas == nil) {
			return fmt.Errorf("worker group %s must set both minReplicas and maxReplicas when autoscaling is disabled", workerGroup.GroupName)
		}
		if workerGroup.MinReplicas != nil && *workerGroup.MinReplicas < 0 {
			return fmt.Errorf("worker group %s has negative minReplicas %d", workerGroup.GroupName, *workerGroup.MinReplicas)
		}
		if workerGroup.MaxReplicas != nil && *workerGroup.MaxReplicas < 0 {
			return fmt.Errorf("worker group %s has negative maxReplicas %d", workerGroup.GroupName, *workerGroup.MaxReplicas)
		}
		if workerGroup.MinReplicas != nil && workerGroup.MaxReplicas != nil {
			if *workerGroup.MinReplicas > *workerGroup.MaxReplicas {
				return fmt.Errorf("worker group %s has minReplicas %d greater than maxReplicas %d", workerGroup.GroupName, *workerGroup.MinReplicas, *workerGroup.MaxReplicas)
			}
		}
		if err := validateRayGroupResources(workerGroup.GroupName, workerGroup.RayStartParams, workerGroup.Resources); err != nil {
			return err
		}
		if err := validateRayGroupLabels(workerGroup.GroupName, workerGroup.RayStartParams, workerGroup.Labels); err != nil {
			return err
		}
		if err := validateWorkerGroupIdleTimeout(workerGroup, spec); err != nil {
			return err
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

	// Validate that RAY_enable_autoscaler_v2 environment variable is not set to "1" or "true" when autoscaler is disabled
	if !isAutoscalingEnabled {
		if envVar, exists := EnvVarByName(RAY_ENABLE_AUTOSCALER_V2, spec.HeadGroupSpec.Template.Spec.Containers[RayContainerIndex].Env); exists {
			if envVar.Value == "1" || envVar.Value == "true" {
				return fmt.Errorf("environment variable %s cannot be set to '%s' when enableInTreeAutoscaling is false. Please set enableInTreeAutoscaling: true to use autoscaler v2", RAY_ENABLE_AUTOSCALER_V2, envVar.Value)
			}
		}
	}

	if isAutoscalingEnabled {
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

	// Validate AutoscalerOptions.IdleTimeoutSeconds (works with both v1 and v2 autoscaler)
	if spec.AutoscalerOptions != nil && spec.AutoscalerOptions.IdleTimeoutSeconds != nil {
		if *spec.AutoscalerOptions.IdleTimeoutSeconds < 0 {
			return fmt.Errorf("autoscalerOptions.idleTimeoutSeconds must be non-negative, got %d", *spec.AutoscalerOptions.IdleTimeoutSeconds)
		}
	}

	if IsAuthEnabled(spec) {
		if spec.RayVersion == "" {
			return fmt.Errorf("authOptions.mode is 'token' but RayVersion was not specified. Ray version 2.52.0 or later is required")
		}

		rayVersion, err := version.ParseGeneric(spec.RayVersion)
		if err != nil {
			return fmt.Errorf("authOptions.mode is 'token' but RayVersion format is invalid: %s, %w", spec.RayVersion, err)
		}

		// Require minimum Ray version 2.52.0
		minVersion := version.MustParseGeneric("2.52.0")
		if rayVersion.LessThan(minVersion) {
			return fmt.Errorf("authOptions.mode is 'token' but minimum Ray version is 2.52.0, got %s", spec.RayVersion)
		}

		if IsK8sAuthEnabled(spec.AuthOptions) {
			minVersion := version.MustParseGeneric("2.55.0")
			if rayVersion.LessThan(minVersion) {
				return fmt.Errorf("authOptions.enableK8sTokenAuth is enabled but minimum Ray version is 2.55.0, got %s", spec.RayVersion)
			}
		}
	} else {
		if IsK8sAuthEnabled(spec.AuthOptions) {
			return fmt.Errorf("authOptions.enableK8sTokenAuth is enabled but authOptions.mode not set to 'token'")
		}
	}

	return nil
}

func ValidateRayJobStatus(rayJob *rayv1.RayJob) error {
	if rayJob.Status.JobDeploymentStatus == rayv1.JobDeploymentStatusWaiting && rayJob.Spec.SubmissionMode != rayv1.InteractiveMode {
		return fmt.Errorf("The RayJob status is invalid: JobDeploymentStatus cannot be `Waiting` when SubmissionMode is not InteractiveMode")
	}
	return nil
}

func ValidateRayJobMetadata(metadata metav1.ObjectMeta) error {
	if len(metadata.Name) > MaxRayJobNameLength {
		return fmt.Errorf("The RayJob metadata is invalid: RayJob name should be no more than %d characters", MaxRayJobNameLength)
	}
	if errs := validation.IsDNS1035Label(metadata.Name); len(errs) > 0 {
		return fmt.Errorf("The RayJob metadata is invalid: RayJob name should be a valid DNS1035 label: %v", errs)
	}
	return nil
}

func ValidateRayJobSpec(rayJob *rayv1.RayJob) error {
	// KubeRay has some limitations for the suspend operation. The limitations are a subset of the limitations of
	// Kueue (https://kueue.sigs.k8s.io/docs/tasks/run_rayjobs/#c-limitations). For example, KubeRay allows users
	// to suspend a RayJob with autoscaling enabled, but Kueue doesn't.
	if rayJob.Spec.Suspend && !rayJob.Spec.ShutdownAfterJobFinishes {
		return fmt.Errorf("The RayJob spec is invalid: a RayJob with shutdownAfterJobFinishes set to false is not allowed to be suspended")
	}

	if rayJob.Spec.TTLSecondsAfterFinished < 0 {
		return fmt.Errorf("The RayJob spec is invalid: TTLSecondsAfterFinished must be a non-negative integer")
	}

	// Validate TTL and deletion strategy together
	if err := validateDeletionConfiguration(rayJob); err != nil {
		return err
	}

	isClusterSelectorMode := len(rayJob.Spec.ClusterSelector) != 0
	if rayJob.Spec.Suspend && isClusterSelectorMode {
		return fmt.Errorf("The RayJob spec is invalid: the ClusterSelector mode doesn't support the suspend operation")
	}
	if rayJob.Spec.RayClusterSpec == nil && !isClusterSelectorMode {
		return fmt.Errorf("The RayJob spec is invalid: one of RayClusterSpec or ClusterSelector must be set")
	}
	if isClusterSelectorMode {
		clusterName := rayJob.Spec.ClusterSelector[RayJobClusterSelectorKey]
		if len(clusterName) == 0 {
			return fmt.Errorf("cluster name in ClusterSelector should not be empty")
		}
		if rayJob.Spec.SubmissionMode == rayv1.SidecarMode {
			return fmt.Errorf("ClusterSelector is not supported in SidecarMode")
		}
		if rayJob.Spec.BackoffLimit != nil && *rayJob.Spec.BackoffLimit > 0 {
			return fmt.Errorf("The RayJob spec is invalid: BackoffLimit is incompatible with ClusterSelector mode")
		}
	}

	// InteractiveMode does not support backoffLimit > 1.
	// When a RayJob fails (e.g., due to a missing script) and retries,
	// spec.JobId remains set, causing the new job to incorrectly transition
	// to Running instead of Waiting or Failed.
	// After discussion, we decided to disallow retries in InteractiveMode
	// to avoid ambiguous state handling and unintended behavior.
	// https://github.com/ray-project/kuberay/issues/3525
	if rayJob.Spec.SubmissionMode == rayv1.InteractiveMode && rayJob.Spec.BackoffLimit != nil && *rayJob.Spec.BackoffLimit > 0 {
		return fmt.Errorf("The RayJob spec is invalid: BackoffLimit is incompatible with InteractiveMode")
	}

	if rayJob.Spec.SubmissionMode == rayv1.SidecarMode {
		if rayJob.Spec.SubmitterPodTemplate != nil {
			return fmt.Errorf("Currently, SidecarMode doesn't support SubmitterPodTemplate")
		}

		if rayJob.Spec.SubmitterConfig != nil {
			return fmt.Errorf("Currently, SidecarMode doesn't support SubmitterConfig")
		}

		if rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.RestartPolicy != "" && rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
			return fmt.Errorf("restartPolicy for head Pod should be Never or unset when using SidecarMode")
		}
	}

	if rayJob.Spec.RayClusterSpec != nil {
		if err := ValidateRayClusterSpec(rayJob.Spec.RayClusterSpec, rayJob.Annotations); err != nil {
			return fmt.Errorf("The RayJob spec is invalid: %w", err)
		}
	}

	// Validate whether RuntimeEnvYAML is a valid YAML string. Note that this only checks its validity
	// as a YAML string, not its adherence to the runtime environment schema.
	if _, err := dashboardclient.UnmarshalRuntimeEnvYAML(rayJob.Spec.RuntimeEnvYAML); err != nil {
		return err
	}
	if rayJob.Spec.ActiveDeadlineSeconds != nil && *rayJob.Spec.ActiveDeadlineSeconds <= 0 {
		return fmt.Errorf("The RayJob spec is invalid: activeDeadlineSeconds must be a positive integer")
	}
	if rayJob.Spec.BackoffLimit != nil && *rayJob.Spec.BackoffLimit < 0 {
		return fmt.Errorf("The RayJob spec is invalid: backoffLimit must be a positive integer")
	}

	return nil
}

func ValidateRayServiceMetadata(metadata metav1.ObjectMeta) error {
	if len(metadata.Name) > MaxRayServiceNameLength {
		return fmt.Errorf("The RayService metadata is invalid: RayService name should be no more than %d characters", MaxRayServiceNameLength)
	}
	if errs := validation.IsDNS1035Label(metadata.Name); len(errs) > 0 {
		return fmt.Errorf("The RayService metadata is invalid: RayService name should be a valid DNS1035 label: %v", errs)
	}

	// Validate initializing timeout annotation if present
	if err := validateInitializingTimeout(metadata.Annotations); err != nil {
		return fmt.Errorf("The RayService metadata is invalid: RayService annotations is invalid: %w", err)
	}

	return nil
}

// validateInitializingTimeout validates the ray.io/initializing-timeout annotation if present.
// Accepts Go duration format (e.g., "5m", "1h") or integer seconds.
// Returns an error if the annotation is present but invalid.
func validateInitializingTimeout(annotations map[string]string) error {
	if annotations == nil {
		return nil
	}

	timeoutStr, exists := annotations[RayServiceInitializingTimeoutAnnotation]
	if !exists || timeoutStr == "" {
		return nil
	}

	// Try parsing as Go duration first (e.g., "30m", "1h")
	if timeout, err := time.ParseDuration(timeoutStr); err == nil {
		if timeout <= 0 {
			return fmt.Errorf("annotation %s must be a positive duration, got: %s", RayServiceInitializingTimeoutAnnotation, timeoutStr)
		}
		return nil
	}

	// Try parsing as integer seconds
	if seconds, err := strconv.Atoi(timeoutStr); err == nil {
		if seconds <= 0 {
			return fmt.Errorf("annotation %s must be a positive integer (seconds), got: %s", RayServiceInitializingTimeoutAnnotation, timeoutStr)
		}
		return nil
	}

	return fmt.Errorf("annotation %s has invalid format: %s. Expected Go duration format (e.g., '5m', '1h') or positive integer seconds", RayServiceInitializingTimeoutAnnotation, timeoutStr)
}

func ValidateRayServiceSpec(rayService *rayv1.RayService) error {
	if err := ValidateRayClusterSpec(&rayService.Spec.RayClusterSpec, rayService.Annotations); err != nil {
		return fmt.Errorf("The RayService spec is invalid: %w", err)
	}

	if headSvc := rayService.Spec.RayClusterSpec.HeadGroupSpec.HeadService; headSvc != nil && headSvc.Name != "" {
		return fmt.Errorf("The RayService spec is invalid: spec.rayClusterConfig.headGroupSpec.headService.metadata.name should not be set")
	}

	// only NewClusterWithIncrementalUpgrade, NewCluster, and None are valid upgradeType
	if rayService.Spec.UpgradeStrategy != nil &&
		rayService.Spec.UpgradeStrategy.Type != nil &&
		*rayService.Spec.UpgradeStrategy.Type != rayv1.RayServiceUpgradeNone &&
		*rayService.Spec.UpgradeStrategy.Type != rayv1.RayServiceNewCluster &&
		*rayService.Spec.UpgradeStrategy.Type != rayv1.RayServiceNewClusterWithIncrementalUpgrade {
		return fmt.Errorf("The RayService spec is invalid: Spec.UpgradeStrategy.Type value %s is invalid, valid options are %s, %s, or %s", *rayService.Spec.UpgradeStrategy.Type, rayv1.RayServiceNewClusterWithIncrementalUpgrade, rayv1.RayServiceNewCluster, rayv1.RayServiceUpgradeNone)
	}

	if rayService.Spec.RayClusterDeletionDelaySeconds != nil &&
		*rayService.Spec.RayClusterDeletionDelaySeconds < 0 {
		return fmt.Errorf("The RayService spec is invalid: Spec.RayClusterDeletionDelaySeconds should be a non-negative integer, got %d", *rayService.Spec.RayClusterDeletionDelaySeconds)
	}

	// If type is NewClusterWithIncrementalUpgrade, validate the ClusterUpgradeOptions
	if IsIncrementalUpgradeEnabled(&rayService.Spec) {
		if err := ValidateClusterUpgradeOptions(rayService); err != nil {
			return fmt.Errorf("The RayService spec is invalid: %w", err)
		}
	}

	return nil
}

func ValidateClusterUpgradeOptions(rayService *rayv1.RayService) error {
	if !IsAutoscalingEnabled(&rayService.Spec.RayClusterSpec) {
		return fmt.Errorf("Ray Autoscaler is required for NewClusterWithIncrementalUpgrade")
	}

	options := rayService.Spec.UpgradeStrategy.ClusterUpgradeOptions
	if options == nil {
		return fmt.Errorf("ClusterUpgradeOptions are required for NewClusterWithIncrementalUpgrade")
	}

	// MaxSurgePercent defaults to 100% if unset.
	if *options.MaxSurgePercent < 0 || *options.MaxSurgePercent > 100 {
		return fmt.Errorf("maxSurgePercent must be between 0 and 100")
	}

	if options.StepSizePercent == nil || *options.StepSizePercent < 0 || *options.StepSizePercent > 100 {
		return fmt.Errorf("stepSizePercent must be between 0 and 100")
	}

	if options.IntervalSeconds == nil || *options.IntervalSeconds <= 0 {
		return fmt.Errorf("intervalSeconds must be greater than 0")
	}

	if options.GatewayClassName == "" {
		return fmt.Errorf("gatewayClassName is required for NewClusterWithIncrementalUpgrade")
	}

	return nil
}

// validateDeletionConfiguration validates both deletion strategy and TTL configuration
func validateDeletionConfiguration(rayJob *rayv1.RayJob) error {
	if !rayJob.Spec.ShutdownAfterJobFinishes && rayJob.Spec.TTLSecondsAfterFinished > 0 {
		return fmt.Errorf("The RayJob spec is invalid: a RayJob with shutdownAfterJobFinishes set to false cannot have TTLSecondsAfterFinished")
	}

	// No strategy block: nothing else to validate.
	if rayJob.Spec.DeletionStrategy == nil {
		return nil
	}

	// Feature gate must be enabled for any strategy usage.
	if !features.Enabled(features.RayJobDeletionPolicy) {
		return fmt.Errorf("RayJobDeletionPolicy feature gate must be enabled to use DeletionStrategy")
	}

	legacyConfigured := rayJob.Spec.DeletionStrategy.OnSuccess != nil || rayJob.Spec.DeletionStrategy.OnFailure != nil
	rulesConfigured := len(rayJob.Spec.DeletionStrategy.DeletionRules) > 0

	// Mutual exclusivity: rules mode forbids shutdown & legacy. (TTL+rules is implicitly invalid because TTL requires shutdown.)
	if rulesConfigured && rayJob.Spec.ShutdownAfterJobFinishes {
		return fmt.Errorf("The RayJob spec is invalid: spec.shutdownAfterJobFinishes and spec.deletionStrategy.deletionRules are mutually exclusive")
	}
	if rulesConfigured && legacyConfigured {
		return fmt.Errorf("The RayJob spec is invalid: Cannot use both legacy onSuccess/onFailure fields and deletionRules simultaneously")
	}

	// Detailed content validation
	if legacyConfigured {
		if err := validateLegacyDeletionPolicies(rayJob); err != nil {
			return err
		}
	} else if rulesConfigured {
		if err := validateDeletionRules(rayJob); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("The RayJob spec is invalid: DeletionStrategy requires either BOTH onSuccess and onFailure, OR the deletionRules field (cannot be empty)")
	}

	return nil
}

// validateDeletionRules validates the deletion rules in the RayJob spec.
// It performs per-rule validations, checks for uniqueness, and ensures logical TTL consistency.
// Errors are collected and returned as a single aggregated error using errors.Join for better user feedback.
func validateDeletionRules(rayJob *rayv1.RayJob) error {
	rules := rayJob.Spec.DeletionStrategy.DeletionRules
	isClusterSelectorMode := len(rayJob.Spec.ClusterSelector) != 0

	// Group TTLs by condition type for cross-rule validation and uniqueness checking.
	// We separate JobStatus and JobDeploymentStatus to avoid confusion.
	rulesByJobStatus := make(map[rayv1.JobStatus]map[rayv1.DeletionPolicyType]int32)
	rulesByJobDeploymentStatus := make(map[rayv1.JobDeploymentStatus]map[rayv1.DeletionPolicyType]int32)
	var errs []error

	// Single pass: Validate each rule individually and group for later consistency checks.
	for i, rule := range rules {
		if err := validateDeletionCondition(&rule.Condition); err != nil {
			errs = append(errs, fmt.Errorf("deletionRules[%d]: %w", i, err))
			continue
		}

		// Contextual validations based on spec.
		if isClusterSelectorMode && (rule.Policy == rayv1.DeleteCluster || rule.Policy == rayv1.DeleteWorkers) {
			errs = append(errs, fmt.Errorf("deletionRules[%d]: DeletionPolicyType '%s' not supported when ClusterSelector is set", i, rule.Policy))
			continue
		}
		if IsAutoscalingEnabled(rayJob.Spec.RayClusterSpec) && rule.Policy == rayv1.DeleteWorkers {
			// TODO (rueian): Support in future Ray versions by checking RayVersion.
			errs = append(errs, fmt.Errorf("deletionRules[%d]: DeletionPolicyType 'DeleteWorkers' not supported with autoscaling enabled", i))
			continue
		}

		// Group valid rule for consistency check.
		if rule.Condition.JobStatus != nil {
			if _, exists := rulesByJobStatus[*rule.Condition.JobStatus]; !exists {
				rulesByJobStatus[*rule.Condition.JobStatus] = make(map[rayv1.DeletionPolicyType]int32)
			}

			// Check for uniqueness of the current deletion rule, which can be identified by the (JobStatus, DeletionPolicyType) pair.
			if _, exists := rulesByJobStatus[*rule.Condition.JobStatus][rule.Policy]; exists {
				errs = append(errs, fmt.Errorf("deletionRules[%d]: duplicate rule for DeletionPolicyType '%s' and JobStatus '%s'", i, rule.Policy, *rule.Condition.JobStatus))
				continue
			}

			rulesByJobStatus[*rule.Condition.JobStatus][rule.Policy] = rule.Condition.TTLSeconds
		} else {
			if _, exists := rulesByJobDeploymentStatus[*rule.Condition.JobDeploymentStatus]; !exists {
				rulesByJobDeploymentStatus[*rule.Condition.JobDeploymentStatus] = make(map[rayv1.DeletionPolicyType]int32)
			}

			// Check for uniqueness of the current deletion rule, which can be identified by the (JobDeploymentStatus, DeletionPolicyType) pair.
			if _, exists := rulesByJobDeploymentStatus[*rule.Condition.JobDeploymentStatus][rule.Policy]; exists {
				errs = append(errs, fmt.Errorf("deletionRules[%d]: duplicate rule for DeletionPolicyType '%s' and JobDeploymentStatus '%s'", i, rule.Policy, *rule.Condition.JobDeploymentStatus))
				continue
			}

			rulesByJobDeploymentStatus[*rule.Condition.JobDeploymentStatus][rule.Policy] = rule.Condition.TTLSeconds
		}
	}

	// Second pass: Validate TTL consistency per JobStatus.
	for jobStatus, policyTTLs := range rulesByJobStatus {
		if err := validateTTLConsistency(policyTTLs, "JobStatus", string(jobStatus)); err != nil {
			errs = append(errs, err)
		}
	}

	// Second pass: Validate TTL consistency per JobDeploymentStatus.
	for jobDeploymentStatus, policyTTLs := range rulesByJobDeploymentStatus {
		if err := validateTTLConsistency(policyTTLs, "JobDeploymentStatus", string(jobDeploymentStatus)); err != nil {
			errs = append(errs, err)
		}
	}

	return errstd.Join(errs...)
}

// validateDeletionCondition ensures exactly one of JobStatus and JobDeploymentStatus is specified and TTLSeconds is non-negative.
func validateDeletionCondition(deletionCondition *rayv1.DeletionCondition) error {
	// Validate that exactly one of JobStatus and JobDeploymentStatus is specified.
	hasJobStatus := deletionCondition.JobStatus != nil
	hasJobDeploymentStatus := deletionCondition.JobDeploymentStatus != nil
	if hasJobStatus && hasJobDeploymentStatus {
		return fmt.Errorf("cannot set both JobStatus and JobDeploymentStatus at the same time")
	}
	if !hasJobStatus && !hasJobDeploymentStatus {
		return fmt.Errorf("exactly one of JobStatus and JobDeploymentStatus must be set")
	}

	// Validate TTL is non-negative.
	if deletionCondition.TTLSeconds < 0 {
		return fmt.Errorf("TTLSeconds must be non-negative")
	}

	return nil
}

// validateTTLConsistency ensures TTLs follow the deletion hierarchy: Workers <= Cluster <= Self.
// (Lower TTL means deletes earlier.)
func validateTTLConsistency(policyTTLs map[rayv1.DeletionPolicyType]int32, conditionType string, conditionValue string) error {
	// Define the required deletion order. TTLs must be non-decreasing along this sequence.
	deletionOrder := []rayv1.DeletionPolicyType{
		rayv1.DeleteWorkers,
		rayv1.DeleteCluster,
		rayv1.DeleteSelf,
	}

	var prevPolicy rayv1.DeletionPolicyType
	var prevTTL int32
	var hasPrev bool

	var errs []error

	for _, policy := range deletionOrder {
		ttl, exists := policyTTLs[policy]
		if !exists {
			continue
		}

		if hasPrev && ttl < prevTTL {
			errs = append(errs, fmt.Errorf(
				"for %s '%s': %s TTL (%d) must be >= %s TTL (%d)",
				conditionType, conditionValue, policy, ttl, prevPolicy, prevTTL,
			))
		}

		prevPolicy = policy
		prevTTL = ttl
		hasPrev = true
	}

	return errstd.Join(errs...)
}

// validateLegacyDeletionPolicies handles validation for the old `onSuccess` and `onFailure` fields.
func validateLegacyDeletionPolicies(rayJob *rayv1.RayJob) error {
	isClusterSelectorMode := len(rayJob.Spec.ClusterSelector) != 0

	// Both policies must be set if using the legacy API.
	if rayJob.Spec.DeletionStrategy.OnSuccess == nil || rayJob.Spec.DeletionStrategy.OnFailure == nil {
		return fmt.Errorf("both DeletionStrategy.OnSuccess and DeletionStrategy.OnFailure must be set when using the legacy deletion policy fields of DeletionStrategy")
	}

	// Validate that the Policy field is set within each policy.
	onSuccessPolicy := rayJob.Spec.DeletionStrategy.OnSuccess
	onFailurePolicy := rayJob.Spec.DeletionStrategy.OnFailure

	if onSuccessPolicy.Policy == nil {
		return fmt.Errorf("the DeletionPolicyType field of DeletionStrategy.OnSuccess cannot be unset when DeletionStrategy is enabled")
	}
	if onFailurePolicy.Policy == nil {
		return fmt.Errorf("the DeletionPolicyType field of DeletionStrategy.OnFailure cannot be unset when DeletionStrategy is enabled")
	}

	if isClusterSelectorMode {
		if *onSuccessPolicy.Policy == rayv1.DeleteCluster || *onSuccessPolicy.Policy == rayv1.DeleteWorkers {
			return fmt.Errorf("the ClusterSelector mode doesn't support DeletionStrategy=%s on success", *onSuccessPolicy.Policy)
		}
		if *onFailurePolicy.Policy == rayv1.DeleteCluster || *onFailurePolicy.Policy == rayv1.DeleteWorkers {
			return fmt.Errorf("the ClusterSelector mode doesn't support DeletionStrategy=%s on failure", *onFailurePolicy.Policy)
		}
	}

	if (*onSuccessPolicy.Policy == rayv1.DeleteWorkers || *onFailurePolicy.Policy == rayv1.DeleteWorkers) && IsAutoscalingEnabled(rayJob.Spec.RayClusterSpec) {
		// TODO (rueian): This can be supported in a future Ray version. We should check the RayVersion once we know it.
		return fmt.Errorf("DeletionStrategy=DeleteWorkers currently does not support RayCluster with autoscaling enabled")
	}

	if rayJob.Spec.ShutdownAfterJobFinishes && (*onSuccessPolicy.Policy == rayv1.DeleteNone || *onFailurePolicy.Policy == rayv1.DeleteNone) {
		return fmt.Errorf("The RayJob spec is invalid: shutdownAfterJobFinshes is set to 'true' while deletion policy is 'DeleteNone'")
	}

	return nil
}

// ValidateRayCronJobSpec validates the RayCronJob specification
func ValidateRayCronJobSpec(rayCronJob *rayv1.RayCronJob) error {
	// Validate cron schedule format
	if _, err := cron.ParseStandard(rayCronJob.Spec.Schedule); err != nil {
		return fmt.Errorf("invalid cron schedule: %w", err)
	}

	// Validate the ray job spec
	rayJob := &rayv1.RayJob{
		Spec: rayCronJob.Spec.JobTemplate,
	}

	if err := ValidateRayJobSpec(rayJob); err != nil {
		return fmt.Errorf("invalid RayJob template: %w", err)
	}

	return nil
}

// validateWorkerGroupIdleTimeout validates the idleTimeoutSeconds field in a worker group spec
func validateWorkerGroupIdleTimeout(workerGroup rayv1.WorkerGroupSpec, spec *rayv1.RayClusterSpec) error {
	idleTimeoutSeconds := workerGroup.IdleTimeoutSeconds
	if idleTimeoutSeconds == nil {
		return nil
	}

	if *idleTimeoutSeconds < 0 {
		return fmt.Errorf("worker group %s: idleTimeoutSeconds must be non-negative, got %d", workerGroup.GroupName, *idleTimeoutSeconds)
	}

	// WorkerGroupSpec.idleTimeoutSeconds only allowed on autoscaler v2
	if IsAutoscalingV2Enabled(spec) {
		return nil
	}

	envVar, exists := EnvVarByName(RAY_ENABLE_AUTOSCALER_V2, spec.HeadGroupSpec.Template.Spec.Containers[RayContainerIndex].Env)
	if exists && (envVar.Value == "1" || envVar.Value == "true") {
		return nil
	}

	return fmt.Errorf("worker group %s: idleTimeoutSeconds is set, but autoscaler v2 is not enabled. Please set .spec.autoscalerOptions.version to 'v2' (or set %s environment variable to 'true' in the head pod if using KubeRay < 1.4.0)", workerGroup.GroupName, RAY_ENABLE_AUTOSCALER_V2)
}
