package utils

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

	// In KubeRay, the Ray container must be the first application container in a head or worker Pod.
	RayContainerIndex = 0

	// Batch scheduling labels
	// TODO(tgaddair): consider making these part of the CRD
	RaySchedulerName     = "ray.io/scheduler-name"
	RayPriorityClassName = "ray.io/priority-class-name"

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
	DefaultClientPort = 10001
	// For Ray >= 1.11.0, "DefaultRedisPort" actually refers to the GCS server port.
	// However, the role of this port is unchanged in Ray APIs like ray.init and ray start.
	// This is the port used by Ray workers and drivers inside the Ray cluster to connect to the Ray head.
	DefaultRedisPort                = 6379
	DefaultDashboardPort            = 8265
	DefaultMetricsPort              = 8080
	DefaultDashboardAgentListenPort = 52365
	DefaultServingPort              = 8000

	ClientPortName    = "client"
	RedisPortName     = "redis"
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
	// The full name will be of the form "${RayCluster_Name}-headless-worker-svc".
	HeadlessServiceSuffix = "headless-worker-svc"

	// Use as container env variable
	RAY_CLUSTER_NAME                        = "RAY_CLUSTER_NAME"
	RAY_IP                                  = "RAY_IP"
	FQ_RAY_IP                               = "FQ_RAY_IP"
	RAY_PORT                                = "RAY_PORT"
	RAY_ADDRESS                             = "RAY_ADDRESS"
	REDIS_PASSWORD                          = "REDIS_PASSWORD"
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
	RAY_NODE_TYPE_NAME = "RAY_NODE_TYPE_NAME"

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

	// Ray core default configurations
	DefaultWorkerRayGcsReconnectTimeoutS = "600"

	LOCAL_HOST = "127.0.0.1"
	// Ray FT default readiness probe values
	DefaultReadinessProbeInitialDelaySeconds = 10
	DefaultReadinessProbeTimeoutSeconds      = 1
	DefaultReadinessProbePeriodSeconds       = 5
	DefaultReadinessProbeSuccessThreshold    = 1
	DefaultReadinessProbeFailureThreshold    = 10
	ServeReadinessProbeFailureThreshold      = 1

	// Ray FT default liveness probe values
	DefaultLivenessProbeInitialDelaySeconds = 30
	DefaultLivenessProbeTimeoutSeconds      = 1
	DefaultLivenessProbePeriodSeconds       = 5
	DefaultLivenessProbeSuccessThreshold    = 1
	DefaultLivenessProbeFailureThreshold    = 120

	// Ray health check related configurations
	// Note: Since the Raylet process and the dashboard agent process are fate-sharing,
	// only one of them needs to be checked. So, RayAgentRayletHealthPath accesses the dashboard agent's API endpoint
	// to check the health of the Raylet process.
	// TODO (kevin85421): Should we take the dashboard process into account?
	RayAgentRayletHealthPath  = "api/local_raylet_healthz"
	RayDashboardGCSHealthPath = "api/gcs_healthz"
	RayServeProxyHealthPath   = "-/healthz"
	BaseWgetHealthCommand     = "wget -T 2 -q -O- http://localhost:%d/%s | grep success"

	// Finalizers for RayJob
	RayJobStopJobFinalizer = "ray.io/rayjob-finalizer"

	// RayNodeHeadGroupLabelValue is the value for the RayNodeGroupLabelKey label on a head node
	RayNodeHeadGroupLabelValue = "headgroup"

	// Telemetry
	KUBERAY_VERSION = "v1.1.1"
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
