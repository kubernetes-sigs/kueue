package utils

import "errors"

const (

	// Default application name
	DefaultServeAppName = "default"
	// Belows used as label key

	// RayOriginatedFromCRNameLabelKey and RayOriginatedFromCRDLabelKey are the labels used to associate the root KubeRay Custom Resource.
	// [Example 1] If we create a RayJob named `myjob`, then (1) the RayCluster and (2) the submitter K8s Job will have a
	// `ray.io/originated-from-cr-name=myjob` and a `ray.io/originated-from-crd=RayJob` label.
	//
	// [Example 2] If we create a RayService named `mysvc`, then (1) the RayCluster and (2) the Kubernetes services managed by the RayService
	// will have a `ray.io/originated-from-cr-name=mysvc` and a `ray.io/originated-from-crd=RayService` label.
	RayOriginatedFromCRNameLabelKey          = "ray.io/originated-from-cr-name"
	RayOriginatedFromCRDLabelKey             = "ray.io/originated-from-crd"
	RayClusterLabelKey                       = "ray.io/cluster"
	RayNodeTypeLabelKey                      = "ray.io/node-type"
	RayNodeGroupLabelKey                     = "ray.io/group"
	RayNodeLabelKey                          = "ray.io/is-ray-node"
	RayIDLabelKey                            = "ray.io/identifier"
	RayClusterServingServiceLabelKey         = "ray.io/serve"
	RayClusterHeadlessServiceLabelKey        = "ray.io/headless-worker-svc"
	HashWithoutReplicasAndWorkersToDeleteKey = "ray.io/hash-without-replicas-and-workers-to-delete"
	NumWorkerGroupsKey                       = "ray.io/num-worker-groups"
	KubeRayVersion                           = "ray.io/kuberay-version"

	// In KubeRay, the Ray container must be the first application container in a head or worker Pod.
	RayContainerIndex = 0

	// Batch scheduling labels
	// TODO(tgaddair): consider making these part of the CRD
	RaySchedulerName                = "ray.io/scheduler-name"
	RayPriorityClassName            = "ray.io/priority-class-name"
	RayClusterGangSchedulingEnabled = "ray.io/gang-scheduling-enabled"

	// Ray GCS FT related annotations
	RayFTEnabledAnnotationKey         = "ray.io/ft-enabled"
	RayExternalStorageNSAnnotationKey = "ray.io/external-storage-namespace"

	// If this annotation is set to "true", the KubeRay operator will not modify the container's command.
	// However, the generated `ray start` command will still be stored in the container's environment variable
	// `KUBERAY_GEN_RAY_START_CMD`.
	RayOverwriteContainerCmdAnnotationKey = "ray.io/overwrite-container-cmd"

	// Finalizers for GCS fault tolerance
	GCSFaultToleranceRedisCleanupFinalizer = "ray.io/gcs-ft-redis-cleanup-finalizer"

	// EnableServeServiceKey is exclusively utilized to indicate if a RayCluster is directly used for serving.
	// See https://github.com/ray-project/kuberay/pull/1672 for more details.
	EnableServeServiceKey  = "ray.io/enable-serve-service"
	EnableServeServiceTrue = "true"

	EnableRayClusterServingServiceTrue  = "true"
	EnableRayClusterServingServiceFalse = "false"

	KubernetesApplicationNameLabelKey = "app.kubernetes.io/name"
	KubernetesCreatedByLabelKey       = "app.kubernetes.io/created-by"

	// Use as separator for pod name, for example, raycluster-small-size-worker-0
	DashSymbol = "-"

	// Use as default port
	DefaultClientPort               = 10001
	DefaultGcsServerPort            = 6379
	DefaultDashboardPort            = 8265
	DefaultMetricsPort              = 8080
	DefaultDashboardAgentListenPort = 52365
	DefaultServingPort              = 8000

	ClientPortName    = "client"
	GcsServerPortName = "gcs-server"
	DashboardPortName = "dashboard"
	MetricsPortName   = "metrics"
	ServingPortName   = "serve"

	// The default AppProtocol for Kubernetes service
	DefaultServiceAppProtocol = "tcp"

	// The default application name
	ApplicationName = "kuberay"

	// The default name for kuberay operator
	ComponentName = "kuberay-operator"

	// The default suffix for Headless Service for multi-host worker groups.
	// The full name will be of the form "${RayCluster_Name}-headless".
	HeadlessServiceSuffix = "headless"

	// Use as container env variable
	RAY_CLUSTER_NAME                        = "RAY_CLUSTER_NAME"
	RAY_IP                                  = "RAY_IP"
	FQ_RAY_IP                               = "FQ_RAY_IP"
	RAY_PORT                                = "RAY_PORT"
	RAY_ADDRESS                             = "RAY_ADDRESS"
	RAY_REDIS_ADDRESS                       = "RAY_REDIS_ADDRESS"
	REDIS_PASSWORD                          = "REDIS_PASSWORD"
	REDIS_USERNAME                          = "REDIS_USERNAME"
	RAY_DASHBOARD_ENABLE_K8S_DISK_USAGE     = "RAY_DASHBOARD_ENABLE_K8S_DISK_USAGE"
	RAY_EXTERNAL_STORAGE_NS                 = "RAY_external_storage_namespace"
	RAY_GCS_RPC_SERVER_RECONNECT_TIMEOUT_S  = "RAY_gcs_rpc_server_reconnect_timeout_s"
	RAY_TIMEOUT_MS_TASK_WAIT_FOR_DEATH_INFO = "RAY_timeout_ms_task_wait_for_death_info"
	RAY_GCS_SERVER_REQUEST_TIMEOUT_SECONDS  = "RAY_gcs_server_request_timeout_seconds"
	RAY_SERVE_KV_TIMEOUT_S                  = "RAY_SERVE_KV_TIMEOUT_S"
	RAY_USAGE_STATS_KUBERAY_IN_USE          = "RAY_USAGE_STATS_KUBERAY_IN_USE"
	RAY_USAGE_STATS_EXTRA_TAGS              = "RAY_USAGE_STATS_EXTRA_TAGS"
	RAYCLUSTER_DEFAULT_REQUEUE_SECONDS_ENV  = "RAYCLUSTER_DEFAULT_REQUEUE_SECONDS_ENV"
	RAYCLUSTER_DEFAULT_REQUEUE_SECONDS      = 300
	KUBERAY_GEN_RAY_START_CMD               = "KUBERAY_GEN_RAY_START_CMD"

	// Environment variables for RayJob submitter Kubernetes Job.
	// Example: ray job submit --address=http://$RAY_DASHBOARD_ADDRESS --submission-id=$RAY_JOB_SUBMISSION_ID ...
	RAY_DASHBOARD_ADDRESS = "RAY_DASHBOARD_ADDRESS"
	RAY_JOB_SUBMISSION_ID = "RAY_JOB_SUBMISSION_ID"

	// Environment variables for Ray Autoscaler V2.
	// The value of RAY_CLOUD_INSTANCE_ID is the Pod name for Autoscaler V2 alpha. This may change in the future.
	RAY_CLOUD_INSTANCE_ID = "RAY_CLOUD_INSTANCE_ID"
	// The value of RAY_NODE_TYPE_NAME is the name of the node group (i.e., the value of the "ray.io/group" label).
	RAY_NODE_TYPE_NAME       = "RAY_NODE_TYPE_NAME"
	RAY_ENABLE_AUTOSCALER_V2 = "RAY_enable_autoscaler_v2"

	// This KubeRay operator environment variable is used to determine if random Pod
	// deletion should be enabled. Note that this only takes effect when autoscaling
	// is enabled for the RayCluster. This is a feature flag for v0.6.0, and will be
	// removed if the default behavior is stable enoguh.
	ENABLE_RANDOM_POD_DELETE = "ENABLE_RANDOM_POD_DELETE"

	// This KubeRay operator environment variable is used to determine if the Redis
	// cleanup Job should be enabled. This is a feature flag for v1.0.0.
	ENABLE_GCS_FT_REDIS_CLEANUP = "ENABLE_GCS_FT_REDIS_CLEANUP"

	// This environment variable for the KubeRay operator is used to determine whether to enable
	// the injection of readiness and liveness probes into Ray head and worker containers.
	// Enabling this feature contributes to the robustness of Ray clusters. It is currently a feature
	// flag for v1.1.0 and will be removed if the behavior proves to be stable enough.
	ENABLE_PROBES_INJECTION = "ENABLE_PROBES_INJECTION"

	// This KubeRay operator environment variable is used to determine
	// if operator should treat OpenShift cluster as Vanilla Kubernetes.
	USE_INGRESS_ON_OPENSHIFT = "USE_INGRESS_ON_OPENSHIFT"

	// If set to true, kuberay creates a normal ClusterIP service for a Ray Head instead of a Headless service.
	ENABLE_RAY_HEAD_CLUSTER_IP_SERVICE = "ENABLE_RAY_HEAD_CLUSTER_IP_SERVICE"

	// If set to true, the RayJob CR itself will be deleted if shutdownAfterJobFinishes is set to true. Note that all resources created by the RayJob CR will be deleted, including the K8s Job.
	DELETE_RAYJOB_CR_AFTER_JOB_FINISHES = "DELETE_RAYJOB_CR_AFTER_JOB_FINISHES"

	// If `JobDeploymentStatus` does not transition to `Complete` or `Failed` within
	// `RAYJOB_DEPLOYMENT_STATUS_TRANSITION_GRACE_PERIOD_SECONDS` seconds after `JobStatus`
	// reaches a terminal state, KubeRay will update `JobDeploymentStatus` to either
	// `Complete` or `Failed` directly.

	// If this occurs, it is likely due to a system-level issue (e.g., a Ray bug) that prevents the
	// `ray job submit` process in the Kubernetes Job submitter from exiting.
	RAYJOB_DEPLOYMENT_STATUS_TRANSITION_GRACE_PERIOD_SECONDS         = "RAYJOB_DEPLOYMENT_STATUS_TRANSITION_GRACE_PERIOD_SECONDS"
	DEFAULT_RAYJOB_DEPLOYMENT_STATUS_TRANSITION_GRACE_PERIOD_SECONDS = 300

	// This environment variable for the KubeRay operator determines whether to enable
	// a login shell by passing the -l option to the container command /bin/bash.
	// The -l flag was added by default before KubeRay v1.4.0, but it is no longer added
	// by default starting with v1.4.0.
	ENABLE_LOGIN_SHELL = "ENABLE_LOGIN_SHELL"

	// Ray core default configurations
	DefaultWorkerRayGcsReconnectTimeoutS = "600"

	LOCAL_HOST = "127.0.0.1"
	// Ray FT default readiness probe values
	DefaultReadinessProbeInitialDelaySeconds = 10
	DefaultReadinessProbeTimeoutSeconds      = 2
	// Probe timeout for Head pod needs to be longer as it queries two endpoints (api/local_raylet_healthz & api/gcs_healthz)
	DefaultHeadReadinessProbeTimeoutSeconds = 5
	DefaultReadinessProbePeriodSeconds      = 5
	DefaultReadinessProbeSuccessThreshold   = 1
	DefaultReadinessProbeFailureThreshold   = 10
	ServeReadinessProbeFailureThreshold     = 1

	// Ray FT default liveness probe values
	DefaultLivenessProbeInitialDelaySeconds = 30
	DefaultLivenessProbeTimeoutSeconds      = 2
	// Probe timeout for Head pod needs to be longer as it queries two endpoints (api/local_raylet_healthz & api/gcs_healthz)
	DefaultHeadLivenessProbeTimeoutSeconds = 5
	DefaultLivenessProbePeriodSeconds      = 5
	DefaultLivenessProbeSuccessThreshold   = 1
	DefaultLivenessProbeFailureThreshold   = 120

	// Ray health check related configurations
	// Note: Since the Raylet process and the dashboard agent process are fate-sharing,
	// only one of them needs to be checked. So, RayAgentRayletHealthPath accesses the dashboard agent's API endpoint
	// to check the health of the Raylet process.
	// TODO (kevin85421): Should we take the dashboard process into account?
	RayAgentRayletHealthPath  = "api/local_raylet_healthz"
	RayDashboardGCSHealthPath = "api/gcs_healthz"
	RayServeProxyHealthPath   = "-/healthz"
	BaseWgetHealthCommand     = "wget --tries 1 -T %d -q -O- http://localhost:%d/%s | grep success"

	// Finalizers for RayJob
	RayJobStopJobFinalizer = "ray.io/rayjob-finalizer"

	// RayNodeHeadGroupLabelValue is the value for the RayNodeGroupLabelKey label on a head node
	RayNodeHeadGroupLabelValue = "headgroup"

	// KUBERAY_VERSION is the build version of KubeRay.
	// The version is included in the RAY_USAGE_STATS_EXTRA_TAGS environment variable
	// as well as the user-agent. This constant is updated before release.
	// TODO: Update KUBERAY_VERSION to be a build-time variable.
	KUBERAY_VERSION = "v1.4.2"

	// KubeRayController represents the value of the default job controller
	KubeRayController = "ray.io/kuberay-operator"

	ServeConfigLRUSize = 1000

	// MaxRayClusterNameLength is the maximum RayCluster name to make sure we don't truncate
	// their k8s service names. Currently, "-serve-svc" is the longest service suffix:
	// 63 - len("-serve-svc") == 53, so the name should not be longer than 53 characters.
	MaxRayClusterNameLength = 53
	// MaxRayServiceNameLength is the maximum RayService name to make sure it pass the RayCluster validation.
	// Minus 6 since we append 6 characters to the RayService name to create the cluster (GenerateRayClusterName).
	MaxRayServiceNameLength = MaxRayClusterNameLength - 6
	// MaxRayJobNameLength is the maximum RayJob name to make sure it pass the RayCluster validation
	// Minus 6 since we append 6 characters to the RayJob name to create the cluster (GenerateRayClusterName).
	MaxRayJobNameLength = MaxRayClusterNameLength - 6
)

type ServiceType string

const (
	HeadService    ServiceType = "headService"
	ServingService ServiceType = "serveService"
)

// RayOriginatedFromCRDLabelValue generates a value for the label RayOriginatedFromCRDLabelKey
// This is also the only function to construct label filter of resources originated from a given CRDType.
func RayOriginatedFromCRDLabelValue(crdType CRDType) string {
	return string(crdType)
}

type errRayClusterReplicaFailure struct {
	reason string
}

func (e *errRayClusterReplicaFailure) Error() string {
	return e.reason
}

// These are markers used by the calculateStatus() for setting the RayClusterReplicaFailure condition.
var (
	ErrFailedDeleteAllPods   = &errRayClusterReplicaFailure{reason: "FailedDeleteAllPods"}
	ErrFailedDeleteHeadPod   = &errRayClusterReplicaFailure{reason: "FailedDeleteHeadPod"}
	ErrFailedCreateHeadPod   = &errRayClusterReplicaFailure{reason: "FailedCreateHeadPod"}
	ErrFailedDeleteWorkerPod = &errRayClusterReplicaFailure{reason: "FailedDeleteWorkerPod"}
	ErrFailedCreateWorkerPod = &errRayClusterReplicaFailure{reason: "FailedCreateWorkerPod"}
)

func RayClusterReplicaFailureReason(err error) string {
	var failure *errRayClusterReplicaFailure
	if errors.As(err, &failure) {
		return failure.reason
	}
	return ""
}

// Currently, KubeRay fires events when failures occur during the creation or deletion of resources.
type K8sEventType string

const (
	// RayCluster event list
	InvalidRayClusterStatus   K8sEventType = "InvalidRayClusterStatus"
	InvalidRayClusterSpec     K8sEventType = "InvalidRayClusterSpec"
	InvalidRayClusterMetadata K8sEventType = "InvalidRayClusterMetadata"
	// Head Pod event list
	CreatedHeadPod        K8sEventType = "CreatedHeadPod"
	FailedToCreateHeadPod K8sEventType = "FailedToCreateHeadPod"
	DeletedHeadPod        K8sEventType = "DeletedHeadPod"
	FailedToDeleteHeadPod K8sEventType = "FailedToDeleteHeadPod"

	// Worker Pod event list
	CreatedWorkerPod                  K8sEventType = "CreatedWorkerPod"
	FailedToCreateWorkerPod           K8sEventType = "FailedToCreateWorkerPod"
	DeletedWorkerPod                  K8sEventType = "DeletedWorkerPod"
	FailedToDeleteWorkerPod           K8sEventType = "FailedToDeleteWorkerPod"
	FailedToDeleteWorkerPodCollection K8sEventType = "FailedToDeleteWorkerPodCollection"

	// Redis Cleanup Job event list
	CreatedRedisCleanupJob        K8sEventType = "CreatedRedisCleanupJob"
	FailedToCreateRedisCleanupJob K8sEventType = "FailedToCreateRedisCleanupJob"

	// RayJob event list
	InvalidRayJobSpec             K8sEventType = "InvalidRayJobSpec"
	InvalidRayJobMetadata         K8sEventType = "InvalidRayJobMetadata"
	InvalidRayJobStatus           K8sEventType = "InvalidRayJobStatus"
	CreatedRayJobSubmitter        K8sEventType = "CreatedRayJobSubmitter"
	DeletedRayJobSubmitter        K8sEventType = "DeletedRayJobSubmitter"
	FailedToCreateRayJobSubmitter K8sEventType = "FailedToCreateRayJobSubmitter"
	FailedToDeleteRayJobSubmitter K8sEventType = "FailedToDeleteRayJobSubmitter"
	CreatedRayCluster             K8sEventType = "CreatedRayCluster"
	UpdatedRayCluster             K8sEventType = "UpdatedRayCluster"
	DeletedRayCluster             K8sEventType = "DeletedRayCluster"
	FailedToCreateRayCluster      K8sEventType = "FailedToCreateRayCluster"
	FailedToDeleteRayCluster      K8sEventType = "FailedToDeleteRayCluster"
	FailedToUpdateRayCluster      K8sEventType = "FailedToUpdateRayCluster"

	// RayService event list
	InvalidRayServiceSpec           K8sEventType = "InvalidRayServiceSpec"
	InvalidRayServiceMetadata       K8sEventType = "InvalidRayServiceMetadata"
	UpdatedHeadPodServeLabel        K8sEventType = "UpdatedHeadPodServeLabel"
	UpdatedServeApplications        K8sEventType = "UpdatedServeApplications"
	FailedToUpdateHeadPodServeLabel K8sEventType = "FailedToUpdateHeadPodServeLabel"
	FailedToUpdateServeApplications K8sEventType = "FailedToUpdateServeApplications"

	// Generic Pod event list
	DeletedPod                  K8sEventType = "DeletedPod"
	FailedToDeletePod           K8sEventType = "FailedToDeletePod"
	FailedToDeletePodCollection K8sEventType = "FailedToDeletePodCollection"

	// Ingress event list
	CreatedIngress        K8sEventType = "CreatedIngress"
	FailedToCreateIngress K8sEventType = "FailedToCreateIngress"

	// Route event list
	CreatedRoute        K8sEventType = "CreatedRoute"
	FailedToCreateRoute K8sEventType = "FailedToCreateRoute"

	// Service event list
	CreatedService        K8sEventType = "CreatedService"
	UpdatedService        K8sEventType = "UpdatedService"
	FailedToCreateService K8sEventType = "FailedToCreateService"
	FailedToUpdateService K8sEventType = "FailedToUpdateService"

	// ServiceAccount event list
	CreatedServiceAccount            K8sEventType = "CreatedServiceAccount"
	FailedToCreateServiceAccount     K8sEventType = "FailedToCreateServiceAccount"
	AutoscalerServiceAccountNotFound K8sEventType = "AutoscalerServiceAccountNotFound"

	// Role event list
	CreatedRole        K8sEventType = "CreatedRole"
	FailedToCreateRole K8sEventType = "FailedToCreateRole"

	// RoleBinding list
	CreatedRoleBinding        K8sEventType = "CreatedRoleBinding"
	FailedToCreateRoleBinding K8sEventType = "FailedToCreateRoleBinding"
)
