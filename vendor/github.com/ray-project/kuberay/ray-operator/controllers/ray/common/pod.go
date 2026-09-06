package common

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/version"
	ctrl "sigs.k8s.io/controller-runtime"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	"github.com/ray-project/kuberay/ray-operator/pkg/features"
)

const (
	SharedMemoryVolumeName      = "shared-mem"
	SharedMemoryVolumeMountPath = "/dev/shm"
	PlasmaDirectoryParamKey     = "plasma-directory"
	RayLogVolumeName            = "ray-logs"
	RayLogVolumeMountPath       = "/tmp/ray"
	AutoscalerContainerName     = "autoscaler"
	RayHeadContainer            = "ray-head"
	ObjectStoreMemoryKey        = "object-store-memory"
	// TODO (davidxia): should be a const in upstream ray-project/ray
	AllowSlowStorageEnvVar = "RAY_OBJECT_STORE_ALLOW_SLOW_STORAGE"
	// If set to true, kuberay auto injects an init container waiting for ray GCS.
	// If false, you will need to inject your own init container to ensure ray GCS is up before the ray workers start.
	EnableInitContainerInjectionEnvKey = "ENABLE_INIT_CONTAINER_INJECTION"
	NeuronCoreContainerResourceName    = "aws.amazon.com/neuroncore"
	NeuronCoreRayResourceName          = "neuron_cores"
	TPUContainerResourceName           = "google.com/tpu"
	TPURayResourceName                 = "TPU"
)

var customAcceleratorToRayResourceMap = map[string]string{
	NeuronCoreContainerResourceName: NeuronCoreRayResourceName,
	TPUContainerResourceName:        TPURayResourceName,
}

// Get the port required to connect to the Ray cluster by worker nodes and drivers
// started within the cluster.
// For Ray >= 1.11.0 this is the GCS server port. For Ray < 1.11.0 it is the Redis port.
func GetHeadPort(headStartParams map[string]string) string {
	if value, ok := headStartParams["port"]; ok {
		return value
	}
	return strconv.Itoa(utils.DefaultGcsServerPort)
}

// Check if overwrites the container command.
func isOverwriteRayContainerCmd(instance rayv1.RayCluster) bool {
	v, ok := instance.Annotations[utils.RayOverwriteContainerCmdAnnotationKey]
	return ok && strings.ToLower(v) == "true"
}

func initTemplateAnnotations(instance rayv1.RayCluster, podTemplate *corev1.PodTemplateSpec) {
	if podTemplate.Annotations == nil {
		podTemplate.Annotations = make(map[string]string)
	}

	if isOverwriteRayContainerCmd(instance) {
		podTemplate.Annotations[utils.RayOverwriteContainerCmdAnnotationKey] = "true"
	}
}

func configureGCSFaultTolerance(podTemplate *corev1.PodTemplateSpec, instance rayv1.RayCluster, rayNodeType rayv1.RayNodeType) {
	// Configure environment variables, annotations, and rayStartParams for GCS fault tolerance.
	// Note that both `podTemplate` and `instance` will be modified.
	ftEnabled := utils.IsGCSFaultToleranceEnabled(&instance.Spec, instance.Annotations)
	if podTemplate.Annotations == nil {
		podTemplate.Annotations = make(map[string]string)
	}

	if rayNodeType == rayv1.HeadNode {
		podTemplate.Annotations[utils.RayFTEnabledAnnotationKey] = strconv.FormatBool(ftEnabled)
	}

	if ftEnabled {
		options := instance.Spec.GcsFaultToleranceOptions
		container := &podTemplate.Spec.Containers[utils.RayContainerIndex]

		// Configure the GCS RPC server reconnect timeout for GCS FT.
		if !utils.EnvVarExists(utils.RAY_GCS_RPC_SERVER_RECONNECT_TIMEOUT_S, container.Env) && rayNodeType == rayv1.WorkerNode {
			// If GCS FT is enabled and RAY_GCS_RPC_SERVER_RECONNECT_TIMEOUT_S is not set, set the worker's
			// RAY_GCS_RPC_SERVER_RECONNECT_TIMEOUT_S to 600s. If the worker cannot reconnect to GCS within
			// 600s, the Raylet will exit the process. By default, the value is 60s, so the head node will
			// crash if the GCS server is down for more than 60s. Typically, the new GCS server will be available
			// in 120 seconds, so we set the timeout to 600s to avoid the worker nodes crashing.
			gcsTimeout := corev1.EnvVar{Name: utils.RAY_GCS_RPC_SERVER_RECONNECT_TIMEOUT_S, Value: utils.DefaultWorkerRayGcsReconnectTimeoutS}
			container.Env = append(container.Env, gcsTimeout)
		}

		// Configure the backend-specific settings for GCS FT on the head Pod.
		if rayNodeType == rayv1.HeadNode {
			if utils.IsGCSFaultToleranceEmbedded(options) {
				configureEmbeddedFT(podTemplate, instance, container)
			} else {
				configureRedisFT(podTemplate, &instance, options, container)
			}
		}
	}
}

// configureRedisFT wires the Redis-backed GCS FT settings (external storage
// namespace, Redis address, username, and password) onto the head container.
func configureRedisFT(podTemplate *corev1.PodTemplateSpec, instance *rayv1.RayCluster, options *rayv1.GcsFaultToleranceOptions, container *corev1.Container) {
	// Configure the external storage namespace for GCS FT.
	storageNS := string(instance.UID)
	if v, ok := instance.Annotations[utils.RayExternalStorageNSAnnotationKey]; ok {
		storageNS = v
	}
	if options != nil && options.ExternalStorageNamespace != "" {
		storageNS = options.ExternalStorageNamespace
	}
	podTemplate.Annotations[utils.RayExternalStorageNSAnnotationKey] = storageNS
	if !utils.EnvVarExists(utils.RAY_EXTERNAL_STORAGE_NS, container.Env) {
		storageNSEnv := corev1.EnvVar{Name: utils.RAY_EXTERNAL_STORAGE_NS, Value: storageNS}
		container.Env = append(container.Env, storageNSEnv)
	}

	if options != nil {
		container.Env = append(container.Env, corev1.EnvVar{
			Name:  utils.RAY_REDIS_ADDRESS,
			Value: options.RedisAddress,
		})
		if options.RedisUsername != nil {
			// Note that `redis-username` will be supported starting from Ray 2.41.
			// If `GcsFaultToleranceOptions.RedisUsername` is set, it will be put into the
			// `REDIS_USERNAME` environment variable later. Here, we use `$REDIS_USERNAME` in
			// rayStartParams to refer to the environment variable.
			instance.Spec.HeadGroupSpec.RayStartParams["redis-username"] = "$REDIS_USERNAME"
			container.Env = append(container.Env, corev1.EnvVar{
				Name:      utils.REDIS_USERNAME,
				Value:     options.RedisUsername.Value,
				ValueFrom: options.RedisUsername.ValueFrom,
			})
		}
		if options.RedisPassword != nil {
			// If `GcsFaultToleranceOptions.RedisPassword` is set, it will be put into the
			// `REDIS_PASSWORD` environment variable later. Here, we use `$REDIS_PASSWORD` in
			// rayStartParams to refer to the environment variable.
			instance.Spec.HeadGroupSpec.RayStartParams["redis-password"] = "$REDIS_PASSWORD"
			container.Env = append(container.Env, corev1.EnvVar{
				Name:      utils.REDIS_PASSWORD,
				Value:     options.RedisPassword.Value,
				ValueFrom: options.RedisPassword.ValueFrom,
			})
		}
	} else {
		// If users directly set the `redis-password` in `rayStartParams` instead of referring
		// to a K8s secret, we need to set the `REDIS_PASSWORD` env var so that the Redis cleanup
		// job can connect to Redis using the password. This is not recommended.
		if !utils.EnvVarExists(utils.REDIS_PASSWORD, container.Env) {
			// setting the REDIS_PASSWORD env var from the params
			redisPasswordEnv := corev1.EnvVar{Name: utils.REDIS_PASSWORD}
			if value, ok := instance.Spec.HeadGroupSpec.RayStartParams["redis-password"]; ok {
				redisPasswordEnv.Value = value
				container.Env = append(container.Env, redisPasswordEnv)
			}
		}
	}
}

// configureEmbeddedFT wires the embedded RocksDB GCS FT settings onto the head
// Pod: selects the RocksDB backend, points it at the mounted persistent volume,
// and mounts the PVC backing the store.
func configureEmbeddedFT(podTemplate *corev1.PodTemplateSpec, instance rayv1.RayCluster, container *corev1.Container) {
	options := instance.Spec.GcsFaultToleranceOptions

	if !utils.EnvVarExists(utils.RAY_GCS_STORAGE, container.Env) {
		container.Env = append(container.Env, corev1.EnvVar{
			Name:  utils.RAY_GCS_STORAGE,
			Value: utils.GCSStorageRocksDBValue,
		})
	}
	if !utils.EnvVarExists(utils.RAY_GCS_STORAGE_PATH, container.Env) {
		container.Env = append(container.Env, corev1.EnvVar{
			Name:  utils.RAY_GCS_STORAGE_PATH,
			Value: utils.GCSStorageMountPath,
		})
	}

	subPath := ""
	if options.Storage != nil {
		subPath = options.Storage.SubPath
	}
	container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
		Name:      utils.GCSStorageVolumeName,
		MountPath: utils.GCSStorageMountPath,
		SubPath:   subPath,
	})
	podTemplate.Spec.Volumes = append(podTemplate.Spec.Volumes, corev1.Volume{
		Name: utils.GCSStorageVolumeName,
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: utils.GetGCSStoragePVCName(&instance),
			},
		},
	})
}

// DefaultHeadPodTemplate sets the config values
func DefaultHeadPodTemplate(ctx context.Context, instance rayv1.RayCluster, headSpec rayv1.HeadGroupSpec, podName string, headPort string) corev1.PodTemplateSpec {
	// TODO (Dmitri) The argument headPort is essentially unused;
	// headPort is passed into setMissingRayStartParams but unused there for the head pod.
	// To mitigate this awkwardness and reduce code redundancy, unify head and worker pod configuration logic.
	podTemplate := headSpec.Template
	if utils.IsDeterministicHeadPodNameEnabled() {
		podTemplate.Name = podName
	} else {
		podTemplate.GenerateName = podName
	}
	// Pods created by RayCluster should be restricted to the namespace of the RayCluster.
	// This ensures privilege of KubeRay users are contained within the namespace of the RayCluster.
	podTemplate.ObjectMeta.Namespace = instance.Namespace

	// Update rayStartParams with top-level Resources for head group.
	updateRayStartParamsResources(ctx, headSpec.RayStartParams, headSpec.Resources)

	// Update --labels` in rayStartParams with top-level Labels for head group.
	updateRayStartParamsLabels(headSpec.RayStartParams, headSpec.Labels)

	// Merge K8s labels from the Pod template and the top-level `Labels` field.
	mergedLabels := mergeLabels(headSpec.Template.ObjectMeta.Labels, headSpec.Labels)
	podTemplate.Labels = labelPod(rayv1.HeadNode, instance.Name, utils.RayNodeHeadGroupLabelValue, mergedLabels)

	headSpec.RayStartParams = setMissingRayStartParams(ctx, headSpec.RayStartParams, rayv1.HeadNode, headPort, "")

	initTemplateAnnotations(instance, &podTemplate)

	// if in-tree autoscaling is enabled, then autoscaler container should be injected into head pod.
	if utils.IsAutoscalingEnabled(&instance.Spec) {
		// The default autoscaler is not compatible with Kubernetes. As a result, we disable
		// the monitor process by default and inject a KubeRay autoscaler side container into the head pod.
		headSpec.RayStartParams["no-monitor"] = "true"
		// set custom service account with proper roles bound.
		// utils.CheckName clips the name to match the behavior of reconcileAutoscalerServiceAccount
		podTemplate.Spec.ServiceAccountName = utils.CheckName(utils.GetHeadGroupServiceAccountName(&instance))
		// Use the same image as Ray head container by default.
		autoscalerImage := podTemplate.Spec.Containers[utils.RayContainerIndex].Image
		// inject autoscaler container into head pod
		autoscalerContainer := BuildAutoscalerContainer(autoscalerImage)

		// Configure RAY_AUTH_TOKEN and RAY_AUTH_MODE if auth is enabled.
		if utils.IsAuthEnabled(&instance.Spec) {
			SetContainerTokenAuthEnvVars(instance.Name, &autoscalerContainer, instance.Spec.AuthOptions)
		}

		// Configure mTLS env vars and volume mount for the autoscaler sidecar.
		// validateTLSOptions rejects forbidden TLS env vars in autoscalerOptions.env,
		// preventing the user from overriding these via the merge below.
		//
		// GCS address alignment: the autoscaler co-located in the head pod reaches GCS
		// via localhost (127.0.0.1) or the head pod IP. Both are always present in the
		// head certificate SANs — 127.0.0.1 is added unconditionally, and the pod IP
		// SAN is guaranteed by the wait-for-tls-ip-san init container (injected by
		// configureTLS below) before any containers, including this sidecar, start.
		// No additional RAY_ADDRESS injection is required.
		if utils.IsTLSEnabled(&instance.Spec) {
			SetContainerTLSConfig(&autoscalerContainer)
		}

		// Merge the user overrides from autoscalerOptions into the autoscaler container config.
		mergeAutoscalerOverrides(&autoscalerContainer, instance.Spec.AutoscalerOptions)
		podTemplate.Spec.Containers = append(podTemplate.Spec.Containers, autoscalerContainer)

		if utils.IsAutoscalingV2Enabled(&instance.Spec) {
			setAutoscalerV2EnvVars(&podTemplate)
			podTemplate.Spec.RestartPolicy = corev1.RestartPolicyNever
		} else if utils.IsAutoscalingV1Enabled(&instance.Spec) {
			setAutoscalerV1EnvVars(&podTemplate)
		}
	}

	configureGCSFaultTolerance(&podTemplate, instance, rayv1.HeadNode)

	// If the metrics port does not exist in the Ray container, add a default one for Prometheus.
	isMetricsPortExists := utils.FindContainerPort(&podTemplate.Spec.Containers[utils.RayContainerIndex], utils.MetricsPortName, -1) != -1
	if !isMetricsPortExists {
		metricsPort := corev1.ContainerPort{
			Name:          utils.MetricsPortName,
			ContainerPort: int32(utils.DefaultMetricsPort),
		}
		podTemplate.Spec.Containers[utils.RayContainerIndex].Ports = append(podTemplate.Spec.Containers[utils.RayContainerIndex].Ports, metricsPort)
	}

	if utils.IsAuthEnabled(&instance.Spec) {
		configureTokenAuth(instance.Name, &podTemplate, instance.Spec.AuthOptions)
	}

	configureTLS(&podTemplate, instance, rayv1.HeadNode)

	if features.Enabled(features.RayClusterHistoryServer) && instance.Spec.HistoryServerOptions != nil && instance.Spec.HistoryServerOptions.CollectorOptions != nil {
		fqdnRayIP := utils.GenerateFQDNServiceName(ctx, instance, instance.Namespace)
		collectorContainer := BuildCollectorContainer(instance.Spec.HistoryServerOptions.CollectorOptions, rayv1.HeadNode, instance.Name, instance.Namespace, fqdnRayIP, instance.Labels)

		// The collector queries the Ray Dashboard, so it needs the same credentials as the Ray
		// container.
		if utils.IsAuthEnabled(&instance.Spec) {
			SetContainerTokenAuthEnvVars(instance.Name, &collectorContainer, instance.Spec.AuthOptions)
		}

		podTemplate.Spec.Containers = append(podTemplate.Spec.Containers, collectorContainer)
	}

	return podTemplate
}

// setAutoscalerV2EnvVars sets env vars for autoscaler v2 in the head node
func setAutoscalerV2EnvVars(podTemplate *corev1.PodTemplateSpec) {
	if podTemplate.Spec.Containers[utils.RayContainerIndex].Env == nil {
		podTemplate.Spec.Containers[utils.RayContainerIndex].Env = []corev1.EnvVar{}
	}

	podTemplate.Spec.Containers[utils.RayContainerIndex].Env = append(podTemplate.Spec.Containers[utils.RayContainerIndex].Env, corev1.EnvVar{
		Name:  utils.RAY_ENABLE_AUTOSCALER_V2,
		Value: "true",
	})
}

// setAutoscalerV1EnvVars sets env vars for autoscaler v1 in the head node
func setAutoscalerV1EnvVars(podTemplate *corev1.PodTemplateSpec) {
	if podTemplate.Spec.Containers[utils.RayContainerIndex].Env == nil {
		podTemplate.Spec.Containers[utils.RayContainerIndex].Env = []corev1.EnvVar{}
	}

	podTemplate.Spec.Containers[utils.RayContainerIndex].Env = append(podTemplate.Spec.Containers[utils.RayContainerIndex].Env, corev1.EnvVar{
		Name:  utils.RAY_ENABLE_AUTOSCALER_V2,
		Value: "false",
	})
}

// configureTokenAuth sets environment variables required for Ray token authentication
func configureTokenAuth(clusterName string, podTemplate *corev1.PodTemplateSpec, authOptions *rayv1.AuthOptions) {
	SetContainerTokenAuthEnvVars(clusterName, &podTemplate.Spec.Containers[utils.RayContainerIndex], authOptions)

	if utils.IsK8sAuthEnabled(authOptions) {
		AddRayTokenVolume(&podTemplate.Spec)
	}

	// For RayJob Sidecar mode, we need to set the auth token for the submitter container.

	// Configure auth token for wait-gcs-ready init container if it exists
	for i, initContainer := range podTemplate.Spec.InitContainers {
		if initContainer.Name != "wait-gcs-ready" {
			continue
		}

		SetContainerTokenAuthEnvVars(clusterName, &podTemplate.Spec.InitContainers[i], authOptions)
	}
}

// AddRayTokenVolume adds a projected service account token volume to the pod spec.
func AddRayTokenVolume(podSpec *corev1.PodSpec) {
	if utils.VolumeExists(utils.RayTokenVolumeName, podSpec.Volumes) {
		return
	}

	podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
		Name: utils.RayTokenVolumeName,
		VolumeSource: corev1.VolumeSource{
			Projected: &corev1.ProjectedVolumeSource{
				Sources: []corev1.VolumeProjection{
					{
						ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
							Path: "token",
						},
					},
				},
			},
		},
	})
}

// SetContainerTokenAuthEnvVars sets Ray authentication env vars for a container.
func SetContainerTokenAuthEnvVars(clusterName string, container *corev1.Container, authOptions *rayv1.AuthOptions) {
	if !utils.EnvVarExists(utils.RAY_AUTH_MODE_ENV_VAR, container.Env) {
		container.Env = append(container.Env, corev1.EnvVar{
			Name:  utils.RAY_AUTH_MODE_ENV_VAR,
			Value: string(rayv1.AuthModeToken),
		})
	}

	if utils.IsK8sAuthEnabled(authOptions) {
		if !utils.EnvVarExists(utils.RAY_ENABLE_K8S_TOKEN_AUTH_ENV_VAR, container.Env) {
			container.Env = append(container.Env, corev1.EnvVar{
				Name:  utils.RAY_ENABLE_K8S_TOKEN_AUTH_ENV_VAR,
				Value: "true",
			})
		}
		if !utils.VolumeMountExists(utils.RayTokenVolumeName, container.VolumeMounts) {
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				Name:      utils.RayTokenVolumeName,
				MountPath: utils.RayTokenMountPath,
				ReadOnly:  true,
			})
		}
	} else {
		secretName := utils.CheckName(clusterName)
		if authOptions != nil && authOptions.SecretName != nil && *authOptions.SecretName != "" {
			secretName = *authOptions.SecretName
		}
		if !utils.EnvVarExists(utils.RAY_AUTH_TOKEN_ENV_VAR, container.Env) {
			container.Env = append(container.Env, corev1.EnvVar{
				Name: utils.RAY_AUTH_TOKEN_ENV_VAR,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
						Key:                  utils.RAY_AUTH_TOKEN_SECRET_KEY,
					},
				},
			})
		}
	}
}

// configureTLS injects mTLS configuration into the pod template.
// Mounts the cert-manager generated TLS secret and sets TLS environment variables.
// Idempotent: skips adding the TLS volume if one with RayTLSVolumeName already exists.
func configureTLS(podTemplate *corev1.PodTemplateSpec, instance rayv1.RayCluster, rayNodeType rayv1.RayNodeType) {
	if !utils.IsTLSEnabled(&instance.Spec) {
		return
	}

	// Get the TLS secret name. cert-manager creates separate secrets for head and worker.
	secretName := utils.GetTLSSecretName(instance.Name, rayNodeType)

	// Add the TLS volume if not already present (avoid duplicates on re-entry).
	hasTLSVolume := false
	for i := range podTemplate.Spec.Volumes {
		if podTemplate.Spec.Volumes[i].Name == utils.RayTLSVolumeName {
			hasTLSVolume = true
			break
		}
	}
	if !hasTLSVolume {
		podTemplate.Spec.Volumes = append(podTemplate.Spec.Volumes, corev1.Volume{
			Name: utils.RayTLSVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: secretName,
				},
			},
		})
	}

	// Inject env vars and volume mount into the Ray container.
	SetContainerTLSConfig(&podTemplate.Spec.Containers[utils.RayContainerIndex])

	// Inject into the wait-gcs-ready init container only (not user-defined init containers).
	for i := range podTemplate.Spec.InitContainers {
		if podTemplate.Spec.InitContainers[i].Name != "wait-gcs-ready" {
			continue
		}
		SetContainerTLSConfig(&podTemplate.Spec.InitContainers[i])
	}

	// Prepend an init container that waits until cert-manager has added the pod's IP to the
	// certificate as an IP SAN. Required for both head and worker pods:
	//   - Head: ensures the cert has the pod IP before GCS starts, so the autoscaler sidecar
	//     and connecting workers are not hit by a TLS SAN mismatch on first connection.
	//   - Worker: GCS (on the head) connects back to each worker's raylet using the worker's
	//     pod IP. If the worker cert does not yet list that IP the TLS handshake fails, GCS
	//     marks the worker dead, and the RayJob fails. Relying on KubeRay pod recreation is
	//     not sufficient because the RayJob itself fails before a retry can succeed.
	certPath := utils.RayTLSCertMountPath + "/tls.crt"
	waitScript := fmt.Sprintf(`CERT="%s"
if [ -z "${POD_IP}" ]; then
  POD_IP=$(hostname -i 2>/dev/null | awk '{print $1}')
fi
if ! command -v openssl >/dev/null 2>&1; then
  echo "openssl not found; cannot verify IP SAN" >&2
  exit 1
fi
echo "Waiting for TLS cert to include IP SAN for ${POD_IP}..."
while true; do
  if openssl x509 -in "${CERT}" -noout -text 2>/dev/null | grep -qE "IP Address:${POD_IP}([^0-9.]|$)"; then
    echo "TLS cert now includes IP SAN for ${POD_IP}"
    exit 0
  fi
  echo "IP SAN for ${POD_IP} not yet in cert, retrying in 5s..."
  sleep 5
done`, certPath)

	waitInitContainer := corev1.Container{
		Name:            "wait-for-tls-ip-san",
		Image:           podTemplate.Spec.Containers[utils.RayContainerIndex].Image,
		ImagePullPolicy: podTemplate.Spec.Containers[utils.RayContainerIndex].ImagePullPolicy,
		Command:         []string{"sh", "-c"},
		Args:            []string{waitScript},
		SecurityContext: podTemplate.Spec.Containers[utils.RayContainerIndex].SecurityContext.DeepCopy(),
		Env: []corev1.EnvVar{
			{
				Name: "POD_IP",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "status.podIP",
					},
				},
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      utils.RayTLSVolumeName,
				MountPath: utils.RayTLSCertMountPath,
				ReadOnly:  true,
			},
		},
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("50m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("50m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
		},
	}
	// Prepend so it runs before wait-gcs-ready; skip if already present.
	for i := range podTemplate.Spec.InitContainers {
		if podTemplate.Spec.InitContainers[i].Name == "wait-for-tls-ip-san" {
			return
		}
	}
	podTemplate.Spec.InitContainers = append([]corev1.Container{waitInitContainer}, podTemplate.Spec.InitContainers...)
}

// SetContainerTLSConfig adds TLS environment variables and volume mount to a container.
// Idempotent: only appends env vars and volume mount if not already present (avoids duplicates).
// Exported so it can be reused by RayJob submitter containers when needed.
func SetContainerTLSConfig(container *corev1.Container) {
	// Add TLS env vars only if not already present.
	// Use a slice (not a map) to ensure deterministic ordering.
	tlsEnvVars := []corev1.EnvVar{
		{Name: utils.RAY_USE_TLS, Value: "1"},
		{Name: utils.RAY_TLS_SERVER_CERT, Value: utils.RayTLSCertMountPath + "/tls.crt"},
		{Name: utils.RAY_TLS_SERVER_KEY, Value: utils.RayTLSCertMountPath + "/tls.key"},
		{Name: utils.RAY_TLS_CA_CERT, Value: utils.RayTLSCertMountPath + "/ca.crt"},
	}
	existingEnvNames := make(map[string]struct{}, len(container.Env))
	for _, e := range container.Env {
		existingEnvNames[e.Name] = struct{}{}
	}
	for _, ev := range tlsEnvVars {
		if _, ok := existingEnvNames[ev.Name]; !ok {
			container.Env = append(container.Env, ev)
		}
	}

	// Add TLS volume mount only if not already present (by name or mount path).
	hasTLSMount := false
	for i := range container.VolumeMounts {
		m := &container.VolumeMounts[i]
		if m.Name == utils.RayTLSVolumeName || m.MountPath == utils.RayTLSCertMountPath {
			hasTLSMount = true
			break
		}
	}
	if !hasTLSMount {
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      utils.RayTLSVolumeName,
			MountPath: utils.RayTLSCertMountPath,
			ReadOnly:  true,
		})
	}
}

func getEnableInitContainerInjection() bool {
	if s := os.Getenv(EnableInitContainerInjectionEnvKey); strings.ToLower(s) == "false" {
		return false
	}
	return true
}

func getEnableProbesInjection() bool {
	if s := os.Getenv(utils.ENABLE_PROBES_INJECTION); strings.ToLower(s) == "false" {
		return false
	}
	return true
}

// DefaultWorkerPodTemplate sets the config values
func DefaultWorkerPodTemplate(ctx context.Context, instance rayv1.RayCluster, workerSpec rayv1.WorkerGroupSpec, podName string, fqdnRayIP string, headPort string, replicaGrpName string, replicaIndex int, numHostIndex int) corev1.PodTemplateSpec {
	podTemplate := workerSpec.Template
	podTemplate.GenerateName = podName
	// Pods created by RayCluster should be restricted to the namespace of the RayCluster.
	// This ensures privilege of KubeRay users are contained within the namespace of the RayCluster.
	podTemplate.ObjectMeta.Namespace = instance.Namespace

	// The Ray worker should only start once the GCS server is ready.
	// only inject init container only when ENABLE_INIT_CONTAINER_INJECTION is true
	enableInitContainerInjection := getEnableInitContainerInjection()

	if enableInitContainerInjection {
		// Do not modify `deepCopyRayContainer` anywhere.
		deepCopyRayContainer := podTemplate.Spec.Containers[utils.RayContainerIndex].DeepCopy()
		initContainer := corev1.Container{
			Name:            "wait-gcs-ready",
			Image:           podTemplate.Spec.Containers[utils.RayContainerIndex].Image,
			ImagePullPolicy: podTemplate.Spec.Containers[utils.RayContainerIndex].ImagePullPolicy,
			Command:         utils.GetContainerCommand([]string{}),
			Args: []string{
				fmt.Sprintf(`
					SECONDS=0
					while true; do
						if (( SECONDS <= 120 )); then
							if ray health-check --address %s:%s > /dev/null 2>&1; then
								echo "GCS is ready."
								break
							fi
							echo "$SECONDS seconds elapsed: Waiting for GCS to be ready."
						else
							if ray health-check --address %s:%s; then
								echo "GCS is ready. Any error messages above can be safely ignored."
								break
							fi
							echo "$SECONDS seconds elapsed: Still waiting for GCS to be ready. For troubleshooting, refer to the FAQ at https://docs.ray.io/en/master/cluster/kubernetes/troubleshooting.html."
						fi
						sleep 5
					done
				`, fqdnRayIP, headPort, fqdnRayIP, headPort),
			},
			SecurityContext: podTemplate.Spec.Containers[utils.RayContainerIndex].SecurityContext.DeepCopy(),
			// This init container requires certain environment variables to establish a secure connection with the Ray head using TLS authentication.
			// Additionally, some of these environment variables may reference files stored in volumes, so we need to include both the `Env` and `VolumeMounts` fields here.
			// For more details, please refer to: https://docs.ray.io/en/latest/ray-core/configure.html#tls-authentication.
			Env:          deepCopyRayContainer.Env,
			VolumeMounts: deepCopyRayContainer.VolumeMounts,
			// If users specify a ResourceQuota for the namespace, the init container needs to specify resources explicitly.
			// GKE's Autopilot does not support GPU-using init containers, so we explicitly specify the resources for the
			// init container instead of reusing the resources of the Ray container.
			Resources: corev1.ResourceRequirements{
				// The init container's resource consumption remains constant, as it solely sends requests to check the GCS status at a fixed frequency.
				// Therefore, hard-coding the resources is acceptable.
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("200m"),
					corev1.ResourceMemory: resource.MustParse("256Mi"),
				},
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("200m"),
					corev1.ResourceMemory: resource.MustParse("256Mi"),
				},
			},
		}
		podTemplate.Spec.InitContainers = append(podTemplate.Spec.InitContainers, initContainer)
	}
	// If the replica of workers is more than 1, `ObjectMeta.Name` may cause name conflict errors.
	// Hence, we set `ObjectMeta.Name` to an empty string, and use GenerateName to prevent name conflicts.
	podTemplate.ObjectMeta.Name = ""

	// Update rayStartParams with top-level Resources for worker group.
	updateRayStartParamsResources(ctx, workerSpec.RayStartParams, workerSpec.Resources)

	// Update --labels` in rayStartParams with top-level Labels for worker group.
	updateRayStartParamsLabels(workerSpec.RayStartParams, workerSpec.Labels)

	// Merge K8s labels from the Pod template and the top-level `Labels` field.
	mergedLabels := mergeLabels(workerSpec.Template.ObjectMeta.Labels, workerSpec.Labels)
	podTemplate.Labels = labelPod(rayv1.WorkerNode, instance.Name, workerSpec.GroupName, mergedLabels)

	// Add additional labels when RayMultihostIndexing is enabled.
	if features.Enabled(features.RayMultiHostIndexing) {
		// The ordered replica index can be used for the single-host, multi-slice case.
		podTemplate.Labels[utils.RayWorkerReplicaIndexKey] = strconv.Itoa(replicaIndex)
		if workerSpec.NumOfHosts > 1 {
			// These labels are specific to multi-host group setup and reconciliation.
			podTemplate.Labels[utils.RayWorkerReplicaNameKey] = replicaGrpName
			podTemplate.Labels[utils.RayHostIndexKey] = strconv.Itoa(numHostIndex)
		}
	}
	workerSpec.RayStartParams = setMissingRayStartParams(ctx, workerSpec.RayStartParams, rayv1.WorkerNode, headPort, fqdnRayIP)

	initTemplateAnnotations(instance, &podTemplate)
	configureGCSFaultTolerance(&podTemplate, instance, rayv1.WorkerNode)

	// If the metrics port does not exist in the Ray container, add a default one for Prometheus.
	isMetricsPortExists := utils.FindContainerPort(&podTemplate.Spec.Containers[utils.RayContainerIndex], utils.MetricsPortName, -1) != -1
	if !isMetricsPortExists {
		metricsPort := corev1.ContainerPort{
			Name:          utils.MetricsPortName,
			ContainerPort: int32(utils.DefaultMetricsPort),
		}
		podTemplate.Spec.Containers[utils.RayContainerIndex].Ports = append(podTemplate.Spec.Containers[utils.RayContainerIndex].Ports, metricsPort)
	}

	if utils.IsAutoscalingEnabled(&instance.Spec) && utils.IsAutoscalingV2Enabled(&instance.Spec) {
		podTemplate.Spec.RestartPolicy = corev1.RestartPolicyNever
	}

	if utils.IsAuthEnabled(&instance.Spec) {
		configureTokenAuth(instance.Name, &podTemplate, instance.Spec.AuthOptions)
	}

	configureTLS(&podTemplate, instance, rayv1.WorkerNode)

	if features.Enabled(features.RayClusterHistoryServer) && instance.Spec.HistoryServerOptions != nil && instance.Spec.HistoryServerOptions.CollectorOptions != nil {
		collectorContainer := BuildCollectorContainer(instance.Spec.HistoryServerOptions.CollectorOptions, rayv1.WorkerNode, instance.Name, instance.Namespace, fqdnRayIP, instance.Labels)

		if utils.IsAuthEnabled(&instance.Spec) {
			SetContainerTokenAuthEnvVars(instance.Name, &collectorContainer, instance.Spec.AuthOptions)
		}

		podTemplate.Spec.Containers = append(podTemplate.Spec.Containers, collectorContainer)
	}

	return podTemplate
}

func supportsUnifiedHealthCheck(rayVersion string) bool {
	v, err := version.ParseGeneric(rayVersion)
	if err != nil {
		return false
	}

	// Ray version 2.53.0 supports a single HTTP health check endpoint.
	minVersion := version.MustParseGeneric("2.53.0")
	return v.AtLeast(minVersion)
}

func initLivenessAndReadinessProbe(rayContainer *corev1.Container, rayNodeType rayv1.RayNodeType, creatorCRDType utils.CRDType, rayStartParams map[string]string, rayVersion string) {
	getPort := func(key string, defaultVal int32) int32 {
		if portStr, ok := rayStartParams[key]; ok {
			// ParseInt with bitSize=32 ensures the value fits in int32
			if port, err := strconv.ParseInt(portStr, 10, 32); err == nil {
				return int32(port)
			}
		}
		return defaultVal
	}

	httpHealthCheck := supportsUnifiedHealthCheck(rayVersion)
	httpHealthCheckAction := &corev1.HTTPGetAction{
		Path: utils.RayNodeHealthPath,
		Port: intstr.IntOrString{
			Type:   intstr.Int,
			IntVal: getPort("dashboard-agent-listen-port", utils.DefaultDashboardAgentListenPort),
		},
	}

	rayAgentRayletHealthCommand := fmt.Sprintf(
		utils.BaseWgetHealthCommand,
		utils.DefaultReadinessProbeTimeoutSeconds,
		getPort("dashboard-agent-listen-port", utils.DefaultDashboardAgentListenPort),
		utils.RayAgentRayletHealthPath,
	)
	rayDashboardGCSHealthCommand := fmt.Sprintf(
		utils.BaseWgetHealthCommand,
		utils.DefaultReadinessProbeFailureThreshold,
		getPort("dashboard-port", utils.DefaultDashboardPort),
		utils.RayDashboardGCSHealthPath,
	)

	// Generally, the liveness and readiness probes perform the same checks.
	// For head node => Check GCS and Raylet status.
	// For worker node => Check Raylet status.
	commands := []string{}
	if rayNodeType == rayv1.HeadNode {
		commands = append(commands, rayAgentRayletHealthCommand, rayDashboardGCSHealthCommand)
	} else {
		commands = append(commands, rayAgentRayletHealthCommand)
	}

	if rayContainer.LivenessProbe == nil {
		probeTimeout := int32(utils.DefaultLivenessProbeTimeoutSeconds)
		if rayNodeType == rayv1.HeadNode {
			probeTimeout = int32(utils.DefaultHeadLivenessProbeTimeoutSeconds)
		}

		rayContainer.LivenessProbe = &corev1.Probe{
			InitialDelaySeconds: utils.DefaultLivenessProbeInitialDelaySeconds,
			TimeoutSeconds:      probeTimeout,
			PeriodSeconds:       utils.DefaultLivenessProbePeriodSeconds,
			SuccessThreshold:    utils.DefaultLivenessProbeSuccessThreshold,
			FailureThreshold:    utils.DefaultLivenessProbeFailureThreshold,
		}
		if httpHealthCheck {
			rayContainer.LivenessProbe.HTTPGet = httpHealthCheckAction
		} else {
			rayContainer.LivenessProbe.Exec = &corev1.ExecAction{Command: []string{"bash", "-c", strings.Join(commands, " && ")}}
		}
	}

	if rayContainer.ReadinessProbe == nil {
		probeTimeout := int32(utils.DefaultReadinessProbeTimeoutSeconds)
		if rayNodeType == rayv1.HeadNode {
			probeTimeout = int32(utils.DefaultHeadReadinessProbeTimeoutSeconds)
		}
		rayContainer.ReadinessProbe = &corev1.Probe{
			InitialDelaySeconds: utils.DefaultReadinessProbeInitialDelaySeconds,
			TimeoutSeconds:      probeTimeout,
			PeriodSeconds:       utils.DefaultReadinessProbePeriodSeconds,
			SuccessThreshold:    utils.DefaultReadinessProbeSuccessThreshold,
			FailureThreshold:    utils.DefaultReadinessProbeFailureThreshold,
		}
		if httpHealthCheck {
			rayContainer.ReadinessProbe.HTTPGet = httpHealthCheckAction
		} else {
			rayContainer.ReadinessProbe.Exec = &corev1.ExecAction{Command: []string{"bash", "-c", strings.Join(commands, " && ")}}
		}

		// For worker Pods serving traffic, we need to add an additional HTTP proxy health check for the readiness probe.
		// Note: head Pod checks the HTTP proxy's health at every rayservice controller reconcile instaed of using readiness probe.
		// See https://github.com/ray-project/kuberay/pull/1808 for reasons.
		if creatorCRDType == utils.RayServiceCRD && rayNodeType == rayv1.WorkerNode {
			rayContainer.ReadinessProbe.FailureThreshold = utils.ServeReadinessProbeFailureThreshold
			rayServeProxyHealthCommand := fmt.Sprintf(
				utils.BaseWgetHealthCommand,
				utils.DefaultReadinessProbeInitialDelaySeconds,
				utils.FindContainerPort(rayContainer, utils.ServingPortName, utils.DefaultServingPort),
				utils.RayServeProxyHealthPath,
			)
			commands = append(commands, rayServeProxyHealthCommand)
			rayContainer.ReadinessProbe.HTTPGet = nil
			rayContainer.ReadinessProbe.Exec = &corev1.ExecAction{Command: []string{"bash", "-c", strings.Join(commands, " && ")}}
		}
	}
}

// BuildPod a pod config
func BuildPod(ctx context.Context, podTemplateSpec corev1.PodTemplateSpec, rayNodeType rayv1.RayNodeType, rayStartParams map[string]string, headPort string, enableRayAutoscaler bool, creatorCRDType utils.CRDType, fqdnRayIP string, defaultContainerEnvs []corev1.EnvVar, rayVersion string) (aPod corev1.Pod) {
	log := ctrl.LoggerFrom(ctx)

	// For Worker Pod: Traffic readiness is determined by the readiness probe.
	// Therefore, the RayClusterServingServiceLabelKey label is not utilized and should always be set to true.
	// For Head Pod: Traffic readiness is determined by the value of the RayClusterServingServiceLabelKey label.
	// Initially, set the label to false and let the rayservice controller to manage its value.
	if creatorCRDType == utils.RayServiceCRD {
		podTemplateSpec.Labels[utils.RayClusterServingServiceLabelKey] = utils.EnableRayClusterServingServiceTrue
		if rayNodeType == rayv1.HeadNode {
			podTemplateSpec.Labels[utils.RayClusterServingServiceLabelKey] = utils.EnableRayClusterServingServiceFalse
		}
	}

	pod := corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Pod",
		},
		ObjectMeta: podTemplateSpec.ObjectMeta,
		Spec:       podTemplateSpec.Spec,
	}

	// Add /dev/shm volumeMount for the object store to avoid performance degradation.
	// Skip injection when users explicitly set plasma-directory.
	if _, ok := rayStartParams[PlasmaDirectoryParamKey]; !ok {
		addEmptyDir(ctx, &pod.Spec.Containers[utils.RayContainerIndex], &pod, SharedMemoryVolumeName, SharedMemoryVolumeMountPath, corev1.StorageMediumMemory)
	} else {
		log.Info("skip /dev/shm volumeMount injection due to explicit plasma-directory", "plasma-directory", rayStartParams[PlasmaDirectoryParamKey])
	}
	if rayNodeType == rayv1.HeadNode && enableRayAutoscaler {
		// The Ray autoscaler writes logs which are read by the Ray head.
		// We need a shared log volume to enable this information flow.
		// Specifically, this is required for the event-logging functionality
		// introduced in https://github.com/ray-project/ray/pull/13434.
		autoscalerContainerIndex := getAutoscalerContainerIndex(pod)
		addEmptyDir(ctx, &pod.Spec.Containers[utils.RayContainerIndex], &pod, RayLogVolumeName, RayLogVolumeMountPath, corev1.StorageMediumDefault)
		addEmptyDir(ctx, &pod.Spec.Containers[autoscalerContainerIndex], &pod, RayLogVolumeName, RayLogVolumeMountPath, corev1.StorageMediumDefault)
	}
	if features.Enabled(features.RayClusterHistoryServer) {
		if collectorContainerIndex := getCollectorContainerIndex(pod); collectorContainerIndex != -1 {
			addEmptyDir(ctx, &pod.Spec.Containers[utils.RayContainerIndex], &pod, RayLogVolumeName, RayLogVolumeMountPath, corev1.StorageMediumDefault)
			addEmptyDir(ctx, &pod.Spec.Containers[collectorContainerIndex], &pod, RayLogVolumeName, RayLogVolumeMountPath, corev1.StorageMediumDefault)
		}
	}

	var cmd, args string
	if len(pod.Spec.Containers[utils.RayContainerIndex].Command) > 0 {
		cmd = convertCmdToString(pod.Spec.Containers[utils.RayContainerIndex].Command)
	}
	if len(pod.Spec.Containers[utils.RayContainerIndex].Args) > 0 {
		cmd += convertCmdToString(pod.Spec.Containers[utils.RayContainerIndex].Args)
	}

	// Increase the open file descriptor limit of the `ray start` process and its child processes.
	// If RAY_START_ULIMIT_OPEN_FILES is set, use its value; otherwise, fallback to 65536.
	ulimitCmd := fmt.Sprintf("ulimit -n ${%s:-65536}", utils.RAY_START_ULIMIT_OPEN_FILES)
	// Generate the `ray start` command.
	rayStartCmd := generateRayStartCommand(ctx, rayNodeType, rayStartParams, pod.Spec.Containers[utils.RayContainerIndex].Resources)

	// Check if overwrites the generated container command or not.
	isOverwriteRayContainerCmd := false
	if v, ok := podTemplateSpec.Annotations[utils.RayOverwriteContainerCmdAnnotationKey]; ok {
		isOverwriteRayContainerCmd = strings.ToLower(v) == "true"
	}

	// TODO (kevin85421): Consider removing the check for the "ray start" string in the future.
	if !isOverwriteRayContainerCmd && !strings.Contains(cmd, "ray start") {
		generatedCmd := fmt.Sprintf("%s; %s", ulimitCmd, rayStartCmd)
		log.Info("BuildPod", "rayNodeType", rayNodeType, "generatedCmd", generatedCmd)
		// replacing the old command
		pod.Spec.Containers[utils.RayContainerIndex].Command = utils.GetContainerCommand([]string{})
		if cmd != "" {
			// If 'ray start' has --block specified, commands after it will not get executed.
			// so we need to put cmd before cont.
			args = fmt.Sprintf("%s && %s", cmd, generatedCmd)
		} else {
			args = generatedCmd
		}

		pod.Spec.Containers[utils.RayContainerIndex].Args = []string{args}
	}

	for index := range pod.Spec.InitContainers {
		setInitContainerEnvVars(&pod.Spec.InitContainers[index], fqdnRayIP)
	}
	setContainerEnvVars(&pod, rayNodeType, fqdnRayIP, headPort, rayStartCmd, creatorCRDType, defaultContainerEnvs)

	// Inject probes into the Ray containers if the user has not explicitly disabled them.
	// The feature flag `ENABLE_PROBES_INJECTION` will be removed if this feature is stable enough.
	enableProbesInjection := getEnableProbesInjection()
	log.Info("Probes injection feature flag", "enabled", enableProbesInjection)
	if enableProbesInjection {
		// Configure the readiness and liveness probes for the Ray container. These probes
		// play a crucial role in KubeRay health checks. Without them, certain failures,
		// such as the Raylet process crashing, may go undetected.
		initLivenessAndReadinessProbe(&pod.Spec.Containers[utils.RayContainerIndex], rayNodeType, creatorCRDType, rayStartParams, rayVersion)
	}

	return pod
}

// BuildAutoscalerContainer builds a Ray autoscaler container which can be appended to the head pod.
func BuildAutoscalerContainer(autoscalerImage string) corev1.Container {
	// autoscalerStartCmd is the command KubeRay generates to start the autoscaler process.
	// It is stored in the KUBERAY_GEN_AUTOSCALER_START_CMD environment variable so that users
	// who override Args via AutoscalerOptions can still reference the generated command, e.g.:
	//   args: ["ulimit -n 65536; $KUBERAY_GEN_AUTOSCALER_START_CMD"]
	// This mirrors the KUBERAY_GEN_RAY_START_CMD pattern for Ray head/worker containers.
	autoscalerStartCmd := "ray kuberay-autoscaler --cluster-name $(RAY_CLUSTER_NAME) --cluster-namespace $(RAY_CLUSTER_NAMESPACE)"

	container := corev1.Container{
		Name:            AutoscalerContainerName,
		Image:           autoscalerImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Env: []corev1.EnvVar{
			{
				Name: utils.RAY_CLUSTER_NAME,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: fmt.Sprintf("metadata.labels['%s']", utils.RayClusterLabelKey),
					},
				},
			},
			{
				Name: utils.RAY_CLUSTER_NAMESPACE,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.namespace",
					},
				},
			},
			{
				Name: "RAY_HEAD_POD_NAME",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.name",
					},
				},
			},
			{
				Name:  "KUBERAY_CRD_VER",
				Value: "v1",
			},
			// utils.KUBERAY_GEN_AUTOSCALER_START_CMD stores the autoscaler start command
			// generated by KubeRay. Users can reference $KUBERAY_GEN_AUTOSCALER_START_CMD
			// in custom Args to preserve the generated command while adding extra logic.
			// See the KUBERAY_GEN_RAY_START_CMD feature for the analogous Ray container pattern.
			{
				Name:  utils.KUBERAY_GEN_AUTOSCALER_START_CMD,
				Value: autoscalerStartCmd,
			},
		},
		Command: utils.GetContainerCommand([]string{}),
		Args: []string{
			autoscalerStartCmd,
		},
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
	}
	return container
}

// Merge the user overrides from autoscalerOptions into the autoscaler container config.
func mergeAutoscalerOverrides(autoscalerContainer *corev1.Container, autoscalerOptions *rayv1.AutoscalerOptions) {
	if autoscalerOptions != nil {
		if autoscalerOptions.Resources != nil {
			autoscalerContainer.Resources = *autoscalerOptions.Resources
		}
		if autoscalerOptions.Image != nil {
			autoscalerContainer.Image = *autoscalerOptions.Image
		}
		if autoscalerOptions.ImagePullPolicy != nil {
			autoscalerContainer.ImagePullPolicy = *autoscalerOptions.ImagePullPolicy
		}
		if len(autoscalerOptions.Env) > 0 {
			autoscalerContainer.Env = append(autoscalerContainer.Env, autoscalerOptions.Env...)
		}
		if len(autoscalerOptions.EnvFrom) > 0 {
			autoscalerContainer.EnvFrom = append(autoscalerContainer.EnvFrom, autoscalerOptions.EnvFrom...)
		}
		if len(autoscalerOptions.VolumeMounts) > 0 {
			autoscalerContainer.VolumeMounts = append(autoscalerContainer.VolumeMounts, autoscalerOptions.VolumeMounts...)
		}
		if autoscalerOptions.SecurityContext != nil {
			autoscalerContainer.SecurityContext = autoscalerOptions.SecurityContext.DeepCopy()
		}
		if len(autoscalerOptions.Command) > 0 {
			autoscalerContainer.Command = autoscalerOptions.Command
		}
		if len(autoscalerOptions.Args) > 0 {
			autoscalerContainer.Args = autoscalerOptions.Args
		}
	}
}

func convertCmdToString(cmdArr []string) (cmd string) {
	cmdAggr := new(bytes.Buffer)
	for _, v := range cmdArr {
		fmt.Fprintf(cmdAggr, " %s ", v)
	}
	return cmdAggr.String()
}

func getAutoscalerContainerIndex(pod corev1.Pod) (autoscalerContainerIndex int) {
	// we identify the autoscaler container based on its name
	for i, container := range pod.Spec.Containers {
		if container.Name == AutoscalerContainerName {
			return i
		}
	}

	// This should be unreachable.
	panic("Autoscaler container not found!")
}

// getCollectorContainerIndex returns the index of the collector container, or -1 if not found.
func getCollectorContainerIndex(pod corev1.Pod) int {
	for i, container := range pod.Spec.Containers {
		if container.Name == utils.CollectorContainerName {
			return i
		}
	}
	return -1
}

// BuildCollectorContainer builds a history server collector container which can be appended to the head and worker pods.
func BuildCollectorContainer(collectorOptions *rayv1.CollectorOptions, nodeType rayv1.RayNodeType, rayClusterName string, rayClusterNamespace string, fqdnRayIP string, labels map[string]string) corev1.Container {
	image := ""
	if collectorOptions.Image != nil {
		image = *collectorOptions.Image
	}
	pullPolicy := corev1.PullIfNotPresent
	if collectorOptions.ImagePullPolicy != nil {
		pullPolicy = *collectorOptions.ImagePullPolicy
	}
	resources := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("200m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("50m"),
			corev1.ResourceMemory: resource.MustParse("64Mi"),
		},
	}
	if collectorOptions.Resources != nil {
		resources = *collectorOptions.Resources
	}

	role := "Worker"
	if nodeType == rayv1.HeadNode {
		role = "Head"
	}

	container := corev1.Container{
		Name:            utils.CollectorContainerName,
		Image:           image,
		ImagePullPolicy: pullPolicy,
		Resources:       resources,
		Env: []corev1.EnvVar{
			{
				Name: utils.POD_IP,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "status.podIP",
					},
				},
			},
			{
				Name:  utils.RAY_CLUSTER_NAME,
				Value: rayClusterName,
			},
			{
				Name:  utils.RAY_CLUSTER_NAMESPACE,
				Value: rayClusterNamespace,
			},
			{
				Name:  utils.RAY_ROLE,
				Value: role,
			},
			{
				Name:  utils.EVENTS_PORT,
				Value: "8084",
			},
		},
	}

	if fqdnRayIP != "" {
		container.Env = append(container.Env, corev1.EnvVar{
			Name:  utils.FQ_RAY_IP,
			Value: fqdnRayIP,
		})
	}

	if labels != nil && labels[utils.RayOriginatedFromCRDLabelKey] != "" {
		container.Env = append(container.Env, corev1.EnvVar{
			Name:  utils.OWNER_KIND,
			Value: labels[utils.RayOriginatedFromCRDLabelKey],
		})
	}

	if labels != nil && labels[utils.RayOriginatedFromCRNameLabelKey] != "" {
		container.Env = append(container.Env, corev1.EnvVar{
			Name:  utils.OWNER_NAME,
			Value: labels[utils.RayOriginatedFromCRNameLabelKey],
		})
	}

	if nodeType == rayv1.HeadNode {
		if !utils.EnvVarExists(utils.RAY_DASHBOARD_ADDRESS, collectorOptions.Env) {
			container.Env = append(container.Env, corev1.EnvVar{
				Name:  utils.RAY_DASHBOARD_ADDRESS,
				Value: "http://localhost:8265",
			})
		}
	}

	if len(collectorOptions.Env) > 0 {
		container.Env = append(container.Env, collectorOptions.Env...)
	}

	return container
}

// labelPod returns the labels for selecting the resources
// belonging to the given RayCluster CR name.
func labelPod(rayNodeType rayv1.RayNodeType, rayClusterName string, groupName string, overrideLabels map[string]string) map[string]string {
	labels := map[string]string{
		utils.RayNodeLabelKey:                   "yes",
		utils.RayClusterLabelKey:                rayClusterName,
		utils.RayNodeTypeLabelKey:               string(rayNodeType),
		utils.RayNodeGroupLabelKey:              groupName,
		utils.RayIDLabelKey:                     utils.CheckLabel(utils.GenerateIdentifier(rayClusterName, rayNodeType)),
		utils.KubernetesApplicationNameLabelKey: utils.ApplicationName,
		utils.KubernetesCreatedByLabelKey:       utils.ComponentName,
	}

	for k, v := range overrideLabels {
		// The following labels are not overridable
		// - ray.io/node-type
		// - ray.io/group
		// - ray.io/cluster
		if k == utils.RayNodeTypeLabelKey || k == utils.RayNodeGroupLabelKey || k == utils.RayClusterLabelKey {
			continue
		}

		labels[k] = v
	}

	return labels
}

func setInitContainerEnvVars(container *corev1.Container, fqdnRayIP string) {
	if len(container.Env) == 0 {
		container.Env = []corev1.EnvVar{}
	}
	// Init containers in both head and worker require FQ_RAY_IP.
	// (1) The head needs FQ_RAY_IP to create a self-signed certificate for its TLS authenticate.
	// (2) The worker needs FQ_RAY_IP to establish a connection with the Ray head.
	container.Env = append(container.Env,
		corev1.EnvVar{Name: utils.FQ_RAY_IP, Value: fqdnRayIP},
		// RAY_IP is deprecated and should be kept for backward compatibility purposes only.
		corev1.EnvVar{Name: utils.RAY_IP, Value: utils.ExtractRayIPFromFQDN(fqdnRayIP)},
	)
}

func setContainerEnvVars(pod *corev1.Pod, rayNodeType rayv1.RayNodeType, fqdnRayIP string, headPort string, rayStartCmd string, creatorCRDType utils.CRDType, defaultContainerEnvs []corev1.EnvVar) {
	// TODO: Audit all environment variables to identify which should not be modified by users.
	container := &pod.Spec.Containers[utils.RayContainerIndex]
	if len(container.Env) == 0 {
		container.Env = []corev1.EnvVar{}
	}

	// Inject default container environment variables from configuration
	for _, defaultEnv := range defaultContainerEnvs {
		if !utils.EnvVarExists(defaultEnv.Name, container.Env) {
			container.Env = append(container.Env, defaultEnv)
		}
	}

	// case 1: head   => Use LOCAL_HOST
	// case 2: worker => Use fqdnRayIP (fully qualified domain name)
	ip := utils.LOCAL_HOST
	if rayNodeType == rayv1.WorkerNode {
		ip = fqdnRayIP
		container.Env = append(container.Env,
			corev1.EnvVar{Name: utils.FQ_RAY_IP, Value: ip},
			// RAY_IP is deprecated and should be kept for backward compatibility purposes only.
			corev1.EnvVar{Name: utils.RAY_IP, Value: utils.ExtractRayIPFromFQDN(ip)},
		)
	}

	// The RAY_CLUSTER_NAME environment variable is managed by KubeRay and should not be set by the user.
	clusterNameEnv := corev1.EnvVar{
		Name: utils.RAY_CLUSTER_NAME,
		ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{
				FieldPath: fmt.Sprintf("metadata.labels['%s']", utils.RayClusterLabelKey),
			},
		},
	}
	container.Env = append(container.Env, clusterNameEnv)

	// The RAY_CLUSTER_NAMESPACE environment variable is managed by KubeRay and should not be set by the user.
	clusterNamespaceEnv := corev1.EnvVar{
		Name: utils.RAY_CLUSTER_NAMESPACE,
		ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{
				FieldPath: "metadata.namespace",
			},
		},
	}
	container.Env = append(container.Env, clusterNamespaceEnv)

	// RAY_CLOUD_INSTANCE_ID is used by Ray Autoscaler V2 (alpha). See https://github.com/ray-project/kuberay/issues/1751 for more details.
	rayCloudInstanceID := corev1.EnvVar{
		Name: utils.RAY_CLOUD_INSTANCE_ID,
		ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{
				FieldPath: "metadata.name",
			},
		},
	}
	container.Env = append(container.Env, rayCloudInstanceID)

	// RAY_NODE_TYPE_NAME is used by Ray Autoscaler V2 (alpha). See https://github.com/ray-project/kuberay/issues/1965 for more details.
	nodeGroupNameEnv := corev1.EnvVar{
		Name: utils.RAY_NODE_TYPE_NAME,
		ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{
				FieldPath: fmt.Sprintf("metadata.labels['%s']", utils.RayNodeGroupLabelKey),
			},
		},
	}
	container.Env = append(container.Env, nodeGroupNameEnv)

	// utils.KUBERAY_GEN_RAY_START_CMD stores the `ray start` command generated by KubeRay.
	// See https://github.com/ray-project/kuberay/issues/1560 for more details.
	generatedRayStartCmdEnv := corev1.EnvVar{Name: utils.KUBERAY_GEN_RAY_START_CMD, Value: rayStartCmd}
	container.Env = append(container.Env, generatedRayStartCmdEnv)

	if !utils.EnvVarExists(utils.RAY_PORT, container.Env) {
		portEnv := corev1.EnvVar{Name: utils.RAY_PORT, Value: headPort}
		container.Env = append(container.Env, portEnv)
	}

	if creatorCRDType == utils.RayServiceCRD {
		// Only add this env for Ray Service cluster to improve service SLA.
		if !utils.EnvVarExists(utils.RAY_TIMEOUT_MS_TASK_WAIT_FOR_DEATH_INFO, container.Env) {
			deathEnv := corev1.EnvVar{Name: utils.RAY_TIMEOUT_MS_TASK_WAIT_FOR_DEATH_INFO, Value: "0"}
			container.Env = append(container.Env, deathEnv)
		}
		if !utils.EnvVarExists(utils.RAY_GCS_SERVER_REQUEST_TIMEOUT_SECONDS, container.Env) {
			gcsTimeoutEnv := corev1.EnvVar{Name: utils.RAY_GCS_SERVER_REQUEST_TIMEOUT_SECONDS, Value: "5"}
			container.Env = append(container.Env, gcsTimeoutEnv)
		}
		if !utils.EnvVarExists(utils.RAY_SERVE_KV_TIMEOUT_S, container.Env) {
			serveKvTimeoutEnv := corev1.EnvVar{Name: utils.RAY_SERVE_KV_TIMEOUT_S, Value: "5"}
			container.Env = append(container.Env, serveKvTimeoutEnv)
		}
	}
	// Setting the RAY_ADDRESS env allows connecting to Ray using ray.init() when connecting
	// from within the cluster.
	if !utils.EnvVarExists(utils.RAY_ADDRESS, container.Env) {
		rayAddress := fmt.Sprintf("%s:%s", ip, headPort)
		addressEnv := corev1.EnvVar{Name: utils.RAY_ADDRESS, Value: rayAddress}
		container.Env = append(container.Env, addressEnv)
	}
	if !utils.EnvVarExists(utils.RAY_USAGE_STATS_KUBERAY_IN_USE, container.Env) {
		usageEnv := corev1.EnvVar{Name: utils.RAY_USAGE_STATS_KUBERAY_IN_USE, Value: "1"}
		container.Env = append(container.Env, usageEnv)
	}
	if rayNodeType == rayv1.HeadNode {
		extraTagsEnv := corev1.EnvVar{
			Name:  utils.RAY_USAGE_STATS_EXTRA_TAGS,
			Value: fmt.Sprintf("kuberay_version=%s;kuberay_crd=%s", utils.KUBERAY_VERSION, string(creatorCRDType)),
		}
		container.Env = append(container.Env, extraTagsEnv)
	}

	if !utils.EnvVarExists(utils.RAY_DASHBOARD_ENABLE_K8S_DISK_USAGE, container.Env) {
		// This flag enables the display of disk usage. Without this flag, the dashboard will not show disk usage.
		container.Env = append(container.Env, corev1.EnvVar{Name: utils.RAY_DASHBOARD_ENABLE_K8S_DISK_USAGE, Value: "1"})
	}

	if features.Enabled(features.RayClusterHistoryServer) && getCollectorContainerIndex(*pod) != -1 {
		if !utils.EnvVarExists(utils.RAY_ENABLE_RAY_EVENT, container.Env) {
			container.Env = append(container.Env, corev1.EnvVar{Name: utils.RAY_ENABLE_RAY_EVENT, Value: "true"})
		}
		if !utils.EnvVarExists(utils.RAY_ENABLE_CORE_WORKER_RAY_EVENT_TO_AGGREGATOR, container.Env) {
			container.Env = append(container.Env, corev1.EnvVar{Name: utils.RAY_ENABLE_CORE_WORKER_RAY_EVENT_TO_AGGREGATOR, Value: "true"})
		}
		if !utils.EnvVarExists(utils.RAY_DASHBOARD_AGGREGATOR_AGENT_EVENTS_EXPORT_ADDR, container.Env) {
			container.Env = append(container.Env, corev1.EnvVar{Name: utils.RAY_DASHBOARD_AGGREGATOR_AGENT_EVENTS_EXPORT_ADDR, Value: "http://localhost:8084/v1/events"})
		}
		if !utils.EnvVarExists(utils.RAY_DASHBOARD_AGGREGATOR_AGENT_EXPOSABLE_EVENT_TYPES, container.Env) {
			container.Env = append(container.Env, corev1.EnvVar{Name: utils.RAY_DASHBOARD_AGGREGATOR_AGENT_EXPOSABLE_EVENT_TYPES, Value: utils.DEFAULT_RAY_EXPOSABLE_EVENT_TYPES})
		}
		if !utils.EnvVarExists(utils.RAY_DASHBOARD_AGGREGATOR_AGENT_PUBLISHER_HTTP_ENDPOINT_EXPOSABLE_EVENT_TYPES, container.Env) {
			container.Env = append(container.Env, corev1.EnvVar{Name: utils.RAY_DASHBOARD_AGGREGATOR_AGENT_PUBLISHER_HTTP_ENDPOINT_EXPOSABLE_EVENT_TYPES, Value: utils.DEFAULT_RAY_EXPOSABLE_EVENT_TYPES})
		}
	}
}

func setMissingRayStartParams(ctx context.Context, rayStartParams map[string]string, nodeType rayv1.RayNodeType, headPort string, fqdnRayIP string) (completeStartParams map[string]string) {
	log := ctrl.LoggerFrom(ctx)
	// Note: The argument headPort is unused for nodeType == rayv1.HeadNode.
	if nodeType == rayv1.WorkerNode {
		if _, ok := rayStartParams["address"]; !ok {
			address := fmt.Sprintf("%s:%s", fqdnRayIP, headPort)
			rayStartParams["address"] = address
		}
	}

	if nodeType == rayv1.HeadNode {
		// Allow incoming connections from all network interfaces for the dashboard by default.
		// The default value of `dashboard-host` is `localhost` which is not accessible from outside the head Pod.
		if _, ok := rayStartParams["dashboard-host"]; !ok {
			rayStartParams["dashboard-host"] = "0.0.0.0"
		}

		// If `autoscaling-config` is not provided in the head Pod's rayStartParams, the `BASE_READONLY_CONFIG`
		// will be used to initialize the monitor with a READONLY autoscaler which only mirrors what the GCS tells it.
		// See `monitor.py` in Ray repository for more details.
		if _, ok := rayStartParams["autoscaling-config"]; ok {
			log.Info("Detect autoscaling-config in head Pod's rayStartParams. " +
				"The monitor process will initialize the monitor with the provided config. " +
				"Please ensure the autoscaler is set to READONLY mode.")
		}
	}

	// Add a metrics port to expose the metrics to Prometheus.
	if _, ok := rayStartParams["metrics-export-port"]; !ok {
		rayStartParams["metrics-export-port"] = fmt.Sprint(utils.DefaultMetricsPort)
	}

	// Add --block option. See https://github.com/ray-project/kuberay/pull/675
	rayStartParams["block"] = "true"

	// Hardcode the dashboard-agent-listen-port to the default value if it is not provided. This is purely a
	// defensive measure; Ray will already use this default value if the flag is not provided.
	// The default value is used by the RayCluster health probe; see https://github.com/ray-project/kuberay/issues/1760
	if _, ok := rayStartParams["dashboard-agent-listen-port"]; !ok {
		rayStartParams["dashboard-agent-listen-port"] = strconv.Itoa(utils.DefaultDashboardAgentListenPort)
	}

	return rayStartParams
}

func generateRayStartCommand(ctx context.Context, nodeType rayv1.RayNodeType, rayStartParams map[string]string, resource corev1.ResourceRequirements) string {
	log := ctrl.LoggerFrom(ctx)

	log.Info("generateRayStartCommand", "nodeType", nodeType, "rayStartParams", rayStartParams, "Ray container resource", resource)
	if _, ok := rayStartParams["num-cpus"]; !ok {
		cpu := resource.Limits[corev1.ResourceCPU]
		if !cpu.IsZero() {
			rayStartParams["num-cpus"] = strconv.FormatInt(cpu.Value(), 10)
		} else {
			// Fall back to CPU request if limit is not specified
			cpu := resource.Requests[corev1.ResourceCPU]
			if !cpu.IsZero() {
				rayStartParams["num-cpus"] = strconv.FormatInt(cpu.Value(), 10)
			}
		}
	}

	if _, ok := rayStartParams["memory"]; !ok {
		memory := resource.Limits[corev1.ResourceMemory]
		if !memory.IsZero() {
			rayStartParams["memory"] = strconv.FormatInt(memory.Value(), 10)
		}
	}

	// Add GPU and custom accelerator resources to rayStartParams if not already present.
	if err := addWellKnownAcceleratorResources(rayStartParams, resource.Limits); err != nil {
		log.Error(err, "failed to add accelerator resources to rayStartParams")
	}

	rayStartCmd := ""
	switch nodeType {
	case rayv1.HeadNode:
		rayStartCmd = fmt.Sprintf("ray start --head %s", convertParamMap(rayStartParams))
	case rayv1.WorkerNode:
		rayStartCmd = fmt.Sprintf("ray start %s", convertParamMap(rayStartParams))
	default:
		log.Error(fmt.Errorf("missing node type"), "a node must be either head or worker")
	}
	log.Info("generateRayStartCommand", "rayStartCmd", rayStartCmd)
	return rayStartCmd
}

func addWellKnownAcceleratorResources(rayStartParams map[string]string, resourceLimits corev1.ResourceList) error {
	if len(resourceLimits) == 0 {
		return nil
	}

	resourcesMap, err := getResourcesMap(rayStartParams)
	if err != nil {
		return fmt.Errorf("failed to get resources map from rayStartParams: %w", err)
	}

	// Flag to track if any custom accelerator resource are present/added in rayStartParams resources.
	isCustomAcceleratorResourceAdded := isCustomAcceleratorPresentInResources(resourcesMap)

	// Create a sorted slice of resource keys
	// Needed for consistent looping and adding first found custom accelerator resource to ray start params
	sortedResourceKeys := getSortedResourceKeys(resourceLimits)

	for _, resourceKeyString := range sortedResourceKeys {
		resourceValue := resourceLimits[corev1.ResourceName(resourceKeyString)]

		// Scan for resource keys of gpus
		if _, ok := rayStartParams["num-gpus"]; !ok {
			if utils.IsGPUResourceKey(resourceKeyString) && !resourceValue.IsZero() {
				rayStartParams["num-gpus"] = strconv.FormatInt(resourceValue.Value(), 10)
			}
		}

		// Add the first encountered custom accelerator resource from the resource limits to the rayStartParams if not already present
		if !isCustomAcceleratorResourceAdded {
			if rayResourceName, ok := customAcceleratorToRayResourceMap[resourceKeyString]; ok && !resourceValue.IsZero() {
				if _, exists := resourcesMap[rayResourceName]; !exists {
					resourcesMap[rayResourceName] = resourceValue.AsApproximateFloat64()

					// Update the resources map in the rayStartParams
					updatedResourcesStr, err := json.Marshal(resourcesMap)
					if err != nil {
						return fmt.Errorf("failed to marshal resources map to string: %w", err)
					}

					rayStartParams["resources"] = fmt.Sprintf("'%s'", updatedResourcesStr)
				}
				isCustomAcceleratorResourceAdded = true
			}
		}
	}

	return nil
}

func isCustomAcceleratorPresentInResources(resourcesMap map[string]float64) bool {
	// Check whether there exists any custom accelerator resources specified as part of rayStartParams
	if len(resourcesMap) > 0 {
		for _, customAcceleratorRayResource := range customAcceleratorToRayResourceMap {
			if _, ok := resourcesMap[customAcceleratorRayResource]; ok {
				return true
			}
		}
	}

	return false
}

func getResourcesMap(rayStartParams map[string]string) (map[string]float64, error) {
	var resources map[string]float64
	if resourcesStr, ok := rayStartParams["resources"]; !ok {
		resources = make(map[string]float64)
	} else {
		// Trim any surrounding quotes (single, double, or backticks) and spaces
		resourcesStr = strings.Trim(resourcesStr, "'\"` ")
		err := json.Unmarshal([]byte(resourcesStr), &resources)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal resources %w", err)
		}
	}
	return resources, nil
}

func getSortedResourceKeys(resourceLimits corev1.ResourceList) []string {
	sortedResourceKeys := make([]string, 0, len(resourceLimits))
	for resourceKey := range resourceLimits {
		sortedResourceKeys = append(sortedResourceKeys, string(resourceKey))
	}
	sort.Strings(sortedResourceKeys)
	return sortedResourceKeys
}

func convertParamMap(rayStartParams map[string]string) (s string) {
	// Order rayStartParams keys for consistent ray start command flags generation
	keys := make([]string, 0, len(rayStartParams))
	for k := range rayStartParams {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	flags := new(bytes.Buffer)
	// All `ray start` CLI flags that accept an explicit boolean value (e.g.
	// `--flag=false`) must be listed here, otherwise a value of "false"
	// gets silently dropped and the flag never reaches `ray start`.
	specialParameterOptions := []string{"log-color", "include-dashboard", "include-log-monitor"}
	for _, option := range keys {
		argument := rayStartParams[option]
		if utils.Contains([]string{"true", "false"}, strings.ToLower(argument)) && !utils.Contains(specialParameterOptions, option) {
			// booleanOptions: do not require any argument. Essentially represent boolean on-off switches.
			if strings.ToLower(argument) == "true" {
				fmt.Fprintf(flags, " --%s ", option)
			}
		} else {
			// parameterOption: require arguments to be provided along with the option.
			fmt.Fprintf(flags, " --%s=%s ", option, argument)
		}
	}
	return flags.String()
}

// addEmptyDir adds an emptyDir volume to the pod and a corresponding volume mount to the container
// Used for a /dev/shm memory mount for object store and for a /tmp/ray disk mount for autoscaler logs.
func addEmptyDir(ctx context.Context, container *corev1.Container, pod *corev1.Pod, volumeName string, volumeMountPath string, storageMedium corev1.StorageMedium) {
	log := ctrl.LoggerFrom(ctx)

	if checkIfVolumeMounted(container, volumeMountPath) {
		log.Info("volume already mounted", "volume", volumeName, "path", volumeMountPath)
		return
	}

	// 1) If needed, create a Volume of type emptyDir and add it to Volumes.
	if !checkIfVolumeExists(pod, volumeName) {
		emptyDirVolume := makeEmptyDirVolume(container, volumeName, storageMedium)
		pod.Spec.Volumes = append(pod.Spec.Volumes, emptyDirVolume)
	}

	// 2) Create a VolumeMount that uses the emptyDir.
	mountedVolume := corev1.VolumeMount{
		MountPath: volumeMountPath,
		Name:      volumeName,
		ReadOnly:  false,
	}
	container.VolumeMounts = append(container.VolumeMounts, mountedVolume)
}

// Format an emptyDir volume.
// When the storage medium is memory, set the size limit based on container resources.
// For other media, don't set a size limit.
func makeEmptyDirVolume(container *corev1.Container, volumeName string, storageMedium corev1.StorageMedium) corev1.Volume {
	var sizeLimit *resource.Quantity
	if storageMedium == corev1.StorageMediumMemory {
		// If using memory, set size limit based on primary container's resources.
		sizeLimit = findMemoryReqOrLimit(*container)
	} else {
		// Otherwise, don't set a limit.
		sizeLimit = nil
	}
	return corev1.Volume{
		Name: volumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{
				Medium:    storageMedium,
				SizeLimit: sizeLimit,
			},
		},
	}
}

// Checks if the container has a volumeMount with the given mount path and if
// the pod has a matching Volume.
func checkIfVolumeMounted(container *corev1.Container, volumeMountPath string) bool {
	for _, mountedVol := range container.VolumeMounts {
		if mountedVol.MountPath == volumeMountPath {
			return true
		}
	}
	return false
}

// Checks if a volume with the given name exists.
func checkIfVolumeExists(pod *corev1.Pod, volumeName string) bool {
	for _, podVolume := range pod.Spec.Volumes {
		if podVolume.Name == volumeName {
			return true
		}
	}
	return false
}

func findMemoryReqOrLimit(container corev1.Container) (res *resource.Quantity) {
	var mem *resource.Quantity
	// check the limits, if they are not set, check the requests.
	if q, ok := container.Resources.Limits[corev1.ResourceMemory]; ok {
		mem = &q
		return mem
	}
	if q, ok := container.Resources.Requests[corev1.ResourceMemory]; ok {
		mem = &q
		return mem
	}
	return nil
}

// updateRayStartParamsLabels reconciles `--labels` in rayStartParams based on group `Labels`.
func updateRayStartParamsLabels(rayStartParams map[string]string, groupLabels map[string]string) {
	if len(groupLabels) == 0 {
		return
	}
	var labels []string
	// Sort label keys for deterministic output.
	keys := make([]string, 0, len(groupLabels))
	for k := range groupLabels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		labels = append(labels, fmt.Sprintf("%s=%s", k, groupLabels[k]))
	}
	rayStartParams["labels"] = strings.Join(labels, ",")
}

// updateRayStartParamsResources reconciles rayStartParams based on the top-level `Resources` field.
func updateRayStartParamsResources(ctx context.Context, rayStartParams map[string]string, groupResources map[string]string) {
	log := ctrl.LoggerFrom(ctx)

	if len(groupResources) == 0 {
		return
	}
	// Override relevant rayStartParams fields to ensure consistency.
	rayResourcesJson := make(map[string]float64)
	for name, quantity := range groupResources {
		q, err := resource.ParseQuantity(quantity)
		if err != nil {
			log.Info("Skipping resource %s: failed to parse quantity '%s': %v", name, quantity, err)
			continue
		}

		// Normalize the resource name to lowercase for all default checks.
		normalizedName := strings.ToLower(name)
		if normalizedName == string(corev1.ResourceCPU) {
			rayStartParams["num-cpus"] = strconv.FormatInt(q.Value(), 10)
		} else if normalizedName == string(corev1.ResourceMemory) {
			rayStartParams["memory"] = strconv.FormatInt(q.Value(), 10)
		} else if utils.IsGPUResourceKey(normalizedName) {
			rayStartParams["num-gpus"] = strconv.FormatInt(q.Value(), 10)
		} else {
			rayResourcesJson[name] = q.AsApproximateFloat64()
		}
	}

	if len(rayResourcesJson) > 0 {
		jsonBytes, err := json.Marshal(rayResourcesJson)
		if err != nil {
			log.Error(err, "Failed to marshal Ray Resources JSON for rayStartParams.")
			return
		}
		rayStartParams["resources"] = fmt.Sprintf("'%s'", string(jsonBytes))
	}
}

// mergeLabels combines labels from a pod template and a group `labels` spec,
// with the top-level labels field taking precedence.
func mergeLabels(templateLabels map[string]string, groupLabels map[string]string) map[string]string {
	merged := make(map[string]string)
	maps.Copy(merged, templateLabels)
	maps.Copy(merged, groupLabels)
	return merged
}
