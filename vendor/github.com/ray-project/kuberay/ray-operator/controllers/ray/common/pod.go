package common

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"

	"k8s.io/apimachinery/pkg/api/resource"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	SharedMemoryVolumeName      = "shared-mem"
	SharedMemoryVolumeMountPath = "/dev/shm"
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
)

// Get the port required to connect to the Ray cluster by worker nodes and drivers
// started within the cluster.
// For Ray >= 1.11.0 this is the GCS server port. For Ray < 1.11.0 it is the Redis port.
func GetHeadPort(headStartParams map[string]string) string {
	var headPort string
	if value, ok := headStartParams["port"]; !ok {
		// using default port
		headPort = strconv.Itoa(utils.DefaultRedisPort)
	} else {
		// setting port from the params
		headPort = value
	}
	return headPort
}

// Check if the RayCluster has GCS fault tolerance enabled.
func IsGCSFaultToleranceEnabled(instance rayv1.RayCluster) bool {
	v, ok := instance.Annotations[utils.RayFTEnabledAnnotationKey]
	return ok && strings.ToLower(v) == "true"
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

	// For now, we just set ray external storage enabled/disabled by checking if FT is enabled/disabled.
	// This may need to be updated in the future.
	if IsGCSFaultToleranceEnabled(instance) {
		podTemplate.Annotations[utils.RayFTEnabledAnnotationKey] = "true"
		// if we have FT enabled, we need to set up a default external storage namespace.
		podTemplate.Annotations[utils.RayExternalStorageNSAnnotationKey] = string(instance.UID)
	} else {
		podTemplate.Annotations[utils.RayFTEnabledAnnotationKey] = "false"
	}

	if isOverwriteRayContainerCmd(instance) {
		podTemplate.Annotations[utils.RayOverwriteContainerCmdAnnotationKey] = "true"
	}
	// set ray external storage namespace if user specified one.
	if instance.Annotations != nil {
		if v, ok := instance.Annotations[utils.RayExternalStorageNSAnnotationKey]; ok {
			podTemplate.Annotations[utils.RayExternalStorageNSAnnotationKey] = v
		}
	}
}

// DefaultHeadPodTemplate sets the config values
func DefaultHeadPodTemplate(ctx context.Context, instance rayv1.RayCluster, headSpec rayv1.HeadGroupSpec, podName string, headPort string) corev1.PodTemplateSpec {
	// TODO (Dmitri) The argument headPort is essentially unused;
	// headPort is passed into setMissingRayStartParams but unused there for the head pod.
	// To mitigate this awkwardness and reduce code redundancy, unify head and worker pod configuration logic.
	podTemplate := headSpec.Template
	podTemplate.GenerateName = podName
	// Pods created by RayCluster should be restricted to the namespace of the RayCluster.
	// This ensures privilege of KubeRay users are contained within the namespace of the RayCluster.
	podTemplate.ObjectMeta.Namespace = instance.Namespace

	if podTemplate.Labels == nil {
		podTemplate.Labels = make(map[string]string)
	}
	podTemplate.Labels = labelPod(rayv1.HeadNode, instance.Name, utils.RayNodeHeadGroupLabelValue, instance.Spec.HeadGroupSpec.Template.ObjectMeta.Labels)
	headSpec.RayStartParams = setMissingRayStartParams(ctx, headSpec.RayStartParams, rayv1.HeadNode, headPort, "")

	initTemplateAnnotations(instance, &podTemplate)

	// if in-tree autoscaling is enabled, then autoscaler container should be injected into head pod.
	if instance.Spec.EnableInTreeAutoscaling != nil && *instance.Spec.EnableInTreeAutoscaling {
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
		// Merge the user overrides from autoscalerOptions into the autoscaler container config.
		mergeAutoscalerOverrides(&autoscalerContainer, instance.Spec.AutoscalerOptions)
		podTemplate.Spec.Containers = append(podTemplate.Spec.Containers, autoscalerContainer)
	}

	// If the metrics port does not exist in the Ray container, add a default one for Prometheus.
	isMetricsPortExists := utils.FindContainerPort(&podTemplate.Spec.Containers[utils.RayContainerIndex], utils.MetricsPortName, -1) != -1
	if !isMetricsPortExists {
		metricsPort := corev1.ContainerPort{
			Name:          utils.MetricsPortName,
			ContainerPort: int32(utils.DefaultMetricsPort),
		}
		podTemplate.Spec.Containers[utils.RayContainerIndex].Ports = append(podTemplate.Spec.Containers[utils.RayContainerIndex].Ports, metricsPort)
	}

	return podTemplate
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
func DefaultWorkerPodTemplate(ctx context.Context, instance rayv1.RayCluster, workerSpec rayv1.WorkerGroupSpec, podName string, fqdnRayIP string, headPort string) corev1.PodTemplateSpec {
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
			Command:         []string{"/bin/bash", "-lc", "--"},
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
							echo "$SECONDS seconds elapsed: Still waiting for GCS to be ready. For troubleshooting, refer to the FAQ at https://github.com/ray-project/kuberay/blob/master/docs/guidance/FAQ.md."
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
	if podTemplate.Labels == nil {
		podTemplate.Labels = make(map[string]string)
	}
	podTemplate.Labels = labelPod(rayv1.WorkerNode, instance.Name, workerSpec.GroupName, workerSpec.Template.ObjectMeta.Labels)
	workerSpec.RayStartParams = setMissingRayStartParams(ctx, workerSpec.RayStartParams, rayv1.WorkerNode, headPort, fqdnRayIP)

	initTemplateAnnotations(instance, &podTemplate)

	// If the metrics port does not exist in the Ray container, add a default one for Prometheus.
	isMetricsPortExists := utils.FindContainerPort(&podTemplate.Spec.Containers[utils.RayContainerIndex], utils.MetricsPortName, -1) != -1
	if !isMetricsPortExists {
		metricsPort := corev1.ContainerPort{
			Name:          utils.MetricsPortName,
			ContainerPort: int32(utils.DefaultMetricsPort),
		}
		podTemplate.Spec.Containers[utils.RayContainerIndex].Ports = append(podTemplate.Spec.Containers[utils.RayContainerIndex].Ports, metricsPort)
	}

	return podTemplate
}

func initLivenessAndReadinessProbe(rayContainer *corev1.Container, rayNodeType rayv1.RayNodeType, creatorCRDType utils.CRDType) {
	rayAgentRayletHealthCommand := fmt.Sprintf(
		utils.BaseWgetHealthCommand,
		utils.DefaultReadinessProbeTimeoutSeconds,
		utils.DefaultDashboardAgentListenPort,
		utils.RayAgentRayletHealthPath,
	)
	rayDashboardGCSHealthCommand := fmt.Sprintf(
		utils.BaseWgetHealthCommand,
		utils.DefaultReadinessProbeFailureThreshold,
		utils.DefaultDashboardPort,
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
		rayContainer.LivenessProbe.Exec = &corev1.ExecAction{Command: []string{"bash", "-c", strings.Join(commands, " && ")}}
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
		rayContainer.ReadinessProbe.Exec = &corev1.ExecAction{Command: []string{"bash", "-c", strings.Join(commands, " && ")}}

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
			rayContainer.ReadinessProbe.Exec = &corev1.ExecAction{Command: []string{"bash", "-c", strings.Join(commands, " && ")}}
		}
	}
}

// BuildPod a pod config
func BuildPod(ctx context.Context, podTemplateSpec corev1.PodTemplateSpec, rayNodeType rayv1.RayNodeType, rayStartParams map[string]string, headPort string, enableRayAutoscaler *bool, creatorCRDType utils.CRDType, fqdnRayIP string) (aPod corev1.Pod) {
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
	addEmptyDir(ctx, &pod.Spec.Containers[utils.RayContainerIndex], &pod, SharedMemoryVolumeName, SharedMemoryVolumeMountPath, corev1.StorageMediumMemory)
	if rayNodeType == rayv1.HeadNode && enableRayAutoscaler != nil && *enableRayAutoscaler {
		// The Ray autoscaler writes logs which are read by the Ray head.
		// We need a shared log volume to enable this information flow.
		// Specifically, this is required for the event-logging functionality
		// introduced in https://github.com/ray-project/ray/pull/13434.
		autoscalerContainerIndex := getAutoscalerContainerIndex(pod)
		addEmptyDir(ctx, &pod.Spec.Containers[utils.RayContainerIndex], &pod, RayLogVolumeName, RayLogVolumeMountPath, corev1.StorageMediumDefault)
		addEmptyDir(ctx, &pod.Spec.Containers[autoscalerContainerIndex], &pod, RayLogVolumeName, RayLogVolumeMountPath, corev1.StorageMediumDefault)
	}

	var cmd, args string
	if len(pod.Spec.Containers[utils.RayContainerIndex].Command) > 0 {
		cmd = convertCmdToString(pod.Spec.Containers[utils.RayContainerIndex].Command)
	}
	if len(pod.Spec.Containers[utils.RayContainerIndex].Args) > 0 {
		cmd += convertCmdToString(pod.Spec.Containers[utils.RayContainerIndex].Args)
	}

	// Increase the open file descriptor limit of the `ray start` process and its child processes to 65536.
	ulimitCmd := "ulimit -n 65536"
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
		pod.Spec.Containers[utils.RayContainerIndex].Command = []string{"/bin/bash", "-lc", "--"}
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
	setContainerEnvVars(&pod, rayNodeType, rayStartParams, fqdnRayIP, headPort, rayStartCmd, creatorCRDType)

	// Inject probes into the Ray containers if the user has not explicitly disabled them.
	// The feature flag `ENABLE_PROBES_INJECTION` will be removed if this feature is stable enough.
	enableProbesInjection := getEnableProbesInjection()
	log.Info("Probes injection feature flag", "enabled", enableProbesInjection)
	if enableProbesInjection {
		// Configure the readiness and liveness probes for the Ray container. These probes
		// play a crucial role in KubeRay health checks. Without them, certain failures,
		// such as the Raylet process crashing, may go undetected.
		initLivenessAndReadinessProbe(&pod.Spec.Containers[utils.RayContainerIndex], rayNodeType, creatorCRDType)
	}

	return pod
}

// BuildAutoscalerContainer builds a Ray autoscaler container which can be appended to the head pod.
func BuildAutoscalerContainer(autoscalerImage string) corev1.Container {
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
				Name: "RAY_CLUSTER_NAMESPACE",
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
		},
		Command: []string{
			"/bin/bash",
			"-lc",
			"--",
		},
		Args: []string{
			"ray kuberay-autoscaler --cluster-name $(RAY_CLUSTER_NAME) --cluster-namespace $(RAY_CLUSTER_NAMESPACE)",
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

// labelPod returns the labels for selecting the resources
// belonging to the given RayCluster CR name.
func labelPod(rayNodeType rayv1.RayNodeType, rayClusterName string, groupName string, labels map[string]string) (ret map[string]string) {
	if labels == nil {
		labels = make(map[string]string)
	}

	ret = map[string]string{
		utils.RayNodeLabelKey:                   "yes",
		utils.RayClusterLabelKey:                rayClusterName,
		utils.RayNodeTypeLabelKey:               string(rayNodeType),
		utils.RayNodeGroupLabelKey:              groupName,
		utils.RayIDLabelKey:                     utils.CheckLabel(utils.GenerateIdentifier(rayClusterName, rayNodeType)),
		utils.KubernetesApplicationNameLabelKey: utils.ApplicationName,
		utils.KubernetesCreatedByLabelKey:       utils.ComponentName,
	}

	for k, v := range ret {
		if k == string(rayNodeType) {
			// overriding invalid values for this label
			if v != string(rayv1.HeadNode) && v != string(rayv1.WorkerNode) {
				labels[k] = v
			}
		}
		if k == utils.RayNodeGroupLabelKey {
			// overriding invalid values for this label
			if v != groupName {
				labels[k] = v
			}
		}
		if _, ok := labels[k]; !ok {
			labels[k] = v
		}
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

func setContainerEnvVars(pod *corev1.Pod, rayNodeType rayv1.RayNodeType, rayStartParams map[string]string, fqdnRayIP string, headPort string, rayStartCmd string, creatorCRDType utils.CRDType) {
	// TODO: Audit all environment variables to identify which should not be modified by users.
	container := &pod.Spec.Containers[utils.RayContainerIndex]
	if len(container.Env) == 0 {
		container.Env = []corev1.EnvVar{}
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
	if !utils.EnvVarExists(utils.REDIS_PASSWORD, container.Env) {
		// setting the REDIS_PASSWORD env var from the params
		redisPasswordEnv := corev1.EnvVar{Name: utils.REDIS_PASSWORD}
		if value, ok := rayStartParams["redis-password"]; ok {
			redisPasswordEnv.Value = value
		}
		container.Env = append(container.Env, redisPasswordEnv)
	}
	if !utils.EnvVarExists(utils.RAY_EXTERNAL_STORAGE_NS, container.Env) {
		// setting the RAY_EXTERNAL_STORAGE_NS env var from the params
		if pod.Annotations != nil {
			if v, ok := pod.Annotations[utils.RayExternalStorageNSAnnotationKey]; ok {
				storageNS := corev1.EnvVar{Name: utils.RAY_EXTERNAL_STORAGE_NS, Value: v}
				container.Env = append(container.Env, storageNS)
			}
		}
	}
	if !utils.EnvVarExists(utils.RAY_GCS_RPC_SERVER_RECONNECT_TIMEOUT_S, container.Env) && rayNodeType == rayv1.WorkerNode {
		// If GCS FT is enabled and RAY_GCS_RPC_SERVER_RECONNECT_TIMEOUT_S is not set, set the worker's
		// RAY_GCS_RPC_SERVER_RECONNECT_TIMEOUT_S to 600s. If the worker cannot reconnect to GCS within
		// 600s, the Raylet will exit the process. By default, the value is 60s, so the head node will
		// crash if the GCS server is down for more than 60s. Typically, the new GCS server will be available
		// in 120 seconds, so we set the timeout to 600s to avoid the worker nodes crashing.
		if ftEnabled := pod.Annotations[utils.RayFTEnabledAnnotationKey] == "true"; ftEnabled {
			gcsTimeout := corev1.EnvVar{Name: utils.RAY_GCS_RPC_SERVER_RECONNECT_TIMEOUT_S, Value: utils.DefaultWorkerRayGcsReconnectTimeoutS}
			container.Env = append(container.Env, gcsTimeout)
		}
	}
	if !utils.EnvVarExists(utils.RAY_DASHBOARD_ENABLE_K8S_DISK_USAGE, container.Env) {
		// This flag enables the display of disk usage. Without this flag, the dashboard will not show disk usage.
		container.Env = append(container.Env, corev1.EnvVar{Name: utils.RAY_DASHBOARD_ENABLE_K8S_DISK_USAGE, Value: "1"})
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
		}
	}

	if _, ok := rayStartParams["memory"]; !ok {
		memory := resource.Limits[corev1.ResourceMemory]
		if !memory.IsZero() {
			rayStartParams["memory"] = strconv.FormatInt(memory.Value(), 10)
		}
	}

	if _, ok := rayStartParams["num-gpus"]; !ok {
		// Scan for resource keys ending with "gpu" like "nvidia.com/gpu".
		for resourceKey, resource := range resource.Limits {
			if strings.HasSuffix(string(resourceKey), "gpu") && !resource.IsZero() {
				rayStartParams["num-gpus"] = strconv.FormatInt(resource.Value(), 10)
				// For now, only support one GPU type. Break on first match.
				break
			}
		}
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

func convertParamMap(rayStartParams map[string]string) (s string) {
	flags := new(bytes.Buffer)
	// specialParameterOptions' arguments can be true or false.
	// For example, --log-color can be auto | false | true.
	specialParameterOptions := []string{"log-color", "include-dashboard"}
	for option, argument := range rayStartParams {
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
	// check the requests, if they are not set, check the limits.
	if q, ok := container.Resources.Requests[corev1.ResourceMemory]; ok {
		mem = &q
		return mem
	}
	if q, ok := container.Resources.Limits[corev1.ResourceMemory]; ok {
		mem = &q
		return mem
	}
	return nil
}
