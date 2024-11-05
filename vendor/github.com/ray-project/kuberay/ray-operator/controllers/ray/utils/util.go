package utils

import (
	"context"
	"crypto/sha1" //nolint:gosec // We are not using this for security purposes
	"encoding/base32"
	"fmt"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"unicode"

	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/controller-runtime/pkg/manager"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/util/json"

	"k8s.io/apimachinery/pkg/util/rand"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
)

const (
	RayClusterSuffix    = "-raycluster-"
	ServeName           = "serve"
	ClusterDomainEnvKey = "CLUSTER_DOMAIN"
	DefaultDomainName   = "cluster.local"
)

// TODO (kevin85421): Define CRDType here rather than constant.go to avoid circular dependency.
type CRDType string

const (
	RayClusterCRD CRDType = "RayCluster"
	RayJobCRD     CRDType = "RayJob"
	RayServiceCRD CRDType = "RayService"
)

var crdMap = map[string]CRDType{
	"RayCluster": RayClusterCRD,
	"RayJob":     RayJobCRD,
	"RayService": RayServiceCRD,
}

func GetCRDType(key string) CRDType {
	if crdType, exists := crdMap[key]; exists {
		return crdType
	}
	return RayClusterCRD
}

// GetClusterDomainName returns cluster's domain name
func GetClusterDomainName() string {
	if domain := os.Getenv(ClusterDomainEnvKey); len(domain) > 0 {
		return domain
	}

	// Return default domain name.
	return DefaultDomainName
}

// IsCreated returns true if pod has been created and is maintained by the API server
func IsCreated(pod *corev1.Pod) bool {
	return pod.Status.Phase != ""
}

func FindHeadPodReadyCondition(headPod *corev1.Pod) metav1.Condition {
	headPodReadyCondition := metav1.Condition{
		Type:   string(rayv1.HeadPodReady),
		Status: metav1.ConditionFalse,
		Reason: rayv1.UnknownReason,
	}

	for _, cond := range headPod.Status.Conditions {
		if cond.Type != corev1.PodReady {
			continue
		}
		// Set the status based on the PodReady condition
		headPodReadyCondition.Status = metav1.ConditionStatus(cond.Status)
		headPodReadyCondition.Message = cond.Message

		// Determine the reason; default to HeadPodRunningAndReady if the headPod is ready but no specific reason is provided
		reason := cond.Reason
		if cond.Status == corev1.ConditionTrue && reason == "" {
			reason = rayv1.HeadPodRunningAndReady
		}

		// Update the reason if it's not empty
		if reason != "" {
			headPodReadyCondition.Reason = reason
		}

		// Since we're only interested in the PodReady condition, break after processing it
		break
	}
	return headPodReadyCondition
}

// IsRunningAndReady returns true if pod is in the PodRunning Phase, if it has a condition of PodReady.
func IsRunningAndReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func CheckRouteName(ctx context.Context, s string, n string) string {
	log := ctrl.LoggerFrom(ctx)

	// 6 chars are consumed at the end with "-head-" + 5 generated.
	// Namespace name will be appended to form: {name}-{namespace} for first host
	//   segment within route
	// 63 - (6 + 5) - (length of namespace name + 1)
	// => 52 - (length of namespace name + 1)
	// => 51 - (length of namespace name)
	maxLength := 51 - len(n)

	if len(s) > maxLength {
		// shorten the name
		log.Info(fmt.Sprintf("route name is too long: len = %v, we will shorten it to = %v\n", len(s), maxLength))
		s = s[:maxLength]
	}

	// Pass through CheckName for remaining string validations
	return CheckName(s)
}

// PodGenerateName returns the value that should be used for a Pod's generateName
// based on the RayCluster name and node type (head or worker).
func PodGenerateName(prefix string, nodeType rayv1.RayNodeType) string {
	maxPrefixLength := 50 // 63 - (max(8,6) + 5 ) // 6 to 8 char are consumed at the end with "-head-" or -worker- + 5 generated.

	var podPrefix string
	if len(prefix) <= maxPrefixLength {
		podPrefix = prefix
	} else {
		podPrefix = prefix[:maxPrefixLength]
	}

	return strings.ToLower(podPrefix + DashSymbol + string(nodeType) + DashSymbol)
}

// CheckName makes sure the name does not start with a numeric value and the total length is < 63 char
func CheckName(s string) string {
	maxLength := 50 // 63 - (max(8,6) + 5 ) // 6 to 8 char are consumed at the end with "-head-" or -worker- + 5 generated.

	if len(s) > maxLength {
		// shorten the name
		offset := int(math.Abs(float64(maxLength) - float64(len(s))))
		fmt.Printf("pod name is too long: len = %v, we will shorten it by offset = %v", len(s), offset)
		s = s[offset:]
	}

	// cannot start with a numeric value
	if unicode.IsDigit(rune(s[0])) {
		s = "r" + s[1:]
	}

	// cannot start with a punctuation
	if unicode.IsPunct(rune(s[0])) {
		fmt.Println(s)
		s = "r" + s[1:]
	}

	return s
}

// CheckLabel makes sure the label value does not start with a punctuation and the total length is < 63 char
func CheckLabel(s string) string {
	maxLenght := 63

	if len(s) > maxLenght {
		// shorten the name
		offset := int(math.Abs(float64(maxLenght) - float64(len(s))))
		fmt.Printf("label value is too long: len = %v, we will shorten it by offset = %v\n", len(s), offset)
		s = s[offset:]
	}

	// cannot start with a punctuation
	if unicode.IsPunct(rune(s[0])) {
		fmt.Println(s)
		s = "r" + s[1:]
	}

	return s
}

// FormatInt returns the string representation of i in the given base,
// for 2 <= base <= 36. The result uses the lower-case letters 'a' to 'z'
// for digit values >= 10.
func FormatInt32(n int32) string {
	return strconv.FormatInt(int64(n), 10)
}

// GetNamespace return namespace
func GetNamespace(metaData metav1.ObjectMeta) string {
	if metaData.Namespace == "" {
		return "default"
	}
	return metaData.Namespace
}

// GenerateHeadServiceName generates a Ray head service name. Note that there are two types of head services:
//
// (1) For RayCluster: If `HeadService.Name` in the cluster spec is not empty, it will be used as the head service name.
// Otherwise, the name is generated based on the RayCluster CR's name.
// (2) For RayService: It's important to note that the RayService CR not only possesses a head service owned by its RayCluster CR
// but also maintains a separate head service for itself to facilitate zero-downtime upgrades. The name of the head service owned
// by the RayService CR is generated based on the RayService CR's name.
//
// @param crdType: The type of the CRD that owns the head service.
// @param clusterSpec: `RayClusterSpec`
// @param ownerName: The name of the CR that owns the head service.
func GenerateHeadServiceName(crdType CRDType, clusterSpec rayv1.RayClusterSpec, ownerName string) (string, error) {
	switch crdType {
	case RayServiceCRD:
		return CheckName(fmt.Sprintf("%s-%s-%s", ownerName, rayv1.HeadNode, "svc")), nil
	case RayClusterCRD:
		headSvcName := CheckName(fmt.Sprintf("%s-%s-%s", ownerName, rayv1.HeadNode, "svc"))
		if clusterSpec.HeadGroupSpec.HeadService != nil && clusterSpec.HeadGroupSpec.HeadService.Name != "" {
			headSvcName = clusterSpec.HeadGroupSpec.HeadService.Name
		}
		return headSvcName, nil
	default:
		return "", fmt.Errorf("unknown CRD type: %s", crdType)
	}
}

// GenerateFQDNServiceName generates a Fully Qualified Domain Name.
func GenerateFQDNServiceName(ctx context.Context, cluster rayv1.RayCluster, namespace string) string {
	log := ctrl.LoggerFrom(ctx)
	headSvcName, err := GenerateHeadServiceName(RayClusterCRD, cluster.Spec, cluster.Name)
	if err != nil {
		log.Error(err, "Failed to generate head service name")
		return ""
	}
	return fmt.Sprintf("%s.%s.svc.%s", headSvcName, namespace, GetClusterDomainName())
}

// ExtractRayIPFromFQDN extracts the head service name (i.e., RAY_IP, deprecated) from a fully qualified
// domain name (FQDN). This function is provided for backward compatibility purposes only.
func ExtractRayIPFromFQDN(fqdnRayIP string) string {
	return strings.Split(fqdnRayIP, ".")[0]
}

// GenerateServeServiceName generates name for serve service.
func GenerateServeServiceName(serviceName string) string {
	return CheckName(fmt.Sprintf("%s-%s-%s", serviceName, ServeName, "svc"))
}

// GenerateServeServiceLabel generates label value for serve service selector.
func GenerateServeServiceLabel(serviceName string) string {
	return fmt.Sprintf("%s-%s", serviceName, ServeName)
}

// GenerateIngressName generates an ingress name from cluster name
func GenerateIngressName(clusterName string) string {
	return fmt.Sprintf("%s-%s-%s", clusterName, rayv1.HeadNode, "ingress")
}

// GenerateRouteName generates an ingress name from cluster name
func GenerateRouteName(clusterName string) string {
	return fmt.Sprintf("%s-%s-%s", clusterName, rayv1.HeadNode, "route")
}

// GenerateRayClusterName generates a ray cluster name from ray service name
func GenerateRayClusterName(serviceName string) string {
	return fmt.Sprintf("%s%s%s", serviceName, RayClusterSuffix, rand.String(5))
}

// GenerateRayJobId generates a ray job id for submission
func GenerateRayJobId(rayjob string) string {
	return fmt.Sprintf("%s-%s", rayjob, rand.String(5))
}

// GenerateIdentifier generates identifier of same group pods
func GenerateIdentifier(clusterName string, nodeType rayv1.RayNodeType) string {
	return fmt.Sprintf("%s-%s", clusterName, nodeType)
}

func GetWorkerGroupDesiredReplicas(ctx context.Context, workerGroupSpec rayv1.WorkerGroupSpec) int32 {
	log := ctrl.LoggerFrom(ctx)
	// Always adhere to min/max replicas constraints.
	var workerReplicas int32
	if *workerGroupSpec.MinReplicas > *workerGroupSpec.MaxReplicas {
		log.Info(fmt.Sprintf("minReplicas (%v) is greater than maxReplicas (%v), using maxReplicas as desired replicas. "+
			"Please fix this to avoid any unexpected behaviors.", *workerGroupSpec.MinReplicas, *workerGroupSpec.MaxReplicas))
		workerReplicas = *workerGroupSpec.MaxReplicas
	} else if workerGroupSpec.Replicas == nil || *workerGroupSpec.Replicas < *workerGroupSpec.MinReplicas {
		// Replicas is impossible to be nil as it has a default value assigned in the CRD.
		// Add this check to make testing easier.
		workerReplicas = *workerGroupSpec.MinReplicas
	} else if *workerGroupSpec.Replicas > *workerGroupSpec.MaxReplicas {
		workerReplicas = *workerGroupSpec.MaxReplicas
	} else {
		workerReplicas = *workerGroupSpec.Replicas
	}
	return workerReplicas
}

// CalculateDesiredReplicas calculate desired worker replicas at the cluster level
func CalculateDesiredReplicas(ctx context.Context, cluster *rayv1.RayCluster) int32 {
	count := int32(0)
	for _, nodeGroup := range cluster.Spec.WorkerGroupSpecs {
		count += GetWorkerGroupDesiredReplicas(ctx, nodeGroup)
	}

	return count
}

// CalculateMinReplicas calculates min worker replicas at the cluster level
func CalculateMinReplicas(cluster *rayv1.RayCluster) int32 {
	count := int32(0)
	for _, nodeGroup := range cluster.Spec.WorkerGroupSpecs {
		count += *nodeGroup.MinReplicas
	}

	return count
}

// CalculateMaxReplicas calculates max worker replicas at the cluster level
func CalculateMaxReplicas(cluster *rayv1.RayCluster) int32 {
	count := int32(0)
	for _, nodeGroup := range cluster.Spec.WorkerGroupSpecs {
		count += *nodeGroup.MaxReplicas
	}

	return count
}

// CalculateReadyReplicas calculates ready worker replicas at the cluster level
// A worker is ready if its Pod has a PodCondition with type == Ready and status == True
func CalculateReadyReplicas(pods corev1.PodList) int32 {
	count := int32(0)
	for _, pod := range pods.Items {
		if val, ok := pod.Labels[RayNodeTypeLabelKey]; !ok || val != string(rayv1.WorkerNode) {
			continue
		}
		if IsRunningAndReady(&pod) {
			count++
		}
	}

	return count
}

// CalculateAvailableReplicas calculates available worker replicas at the cluster level
// A worker is available if its Pod is running
func CalculateAvailableReplicas(pods corev1.PodList) int32 {
	count := int32(0)
	for _, pod := range pods.Items {
		if val, ok := pod.Labels["ray.io/node-type"]; !ok || val != string(rayv1.WorkerNode) {
			continue
		}
		if pod.Status.Phase == corev1.PodRunning {
			count++
		}
	}

	return count
}

func CalculateDesiredResources(cluster *rayv1.RayCluster) corev1.ResourceList {
	desiredResourcesList := []corev1.ResourceList{{}}
	headPodResource := CalculatePodResource(cluster.Spec.HeadGroupSpec.Template.Spec)
	desiredResourcesList = append(desiredResourcesList, headPodResource)
	for _, nodeGroup := range cluster.Spec.WorkerGroupSpecs {
		podResource := CalculatePodResource(nodeGroup.Template.Spec)
		for i := int32(0); i < *nodeGroup.Replicas; i++ {
			desiredResourcesList = append(desiredResourcesList, podResource)
		}
	}
	return sumResourceList(desiredResourcesList)
}

func CalculateMinResources(cluster *rayv1.RayCluster) corev1.ResourceList {
	minResourcesList := []corev1.ResourceList{{}}
	headPodResource := CalculatePodResource(cluster.Spec.HeadGroupSpec.Template.Spec)
	minResourcesList = append(minResourcesList, headPodResource)
	for _, nodeGroup := range cluster.Spec.WorkerGroupSpecs {
		podResource := CalculatePodResource(nodeGroup.Template.Spec)
		for i := int32(0); i < *nodeGroup.MinReplicas; i++ {
			minResourcesList = append(minResourcesList, podResource)
		}
	}
	return sumResourceList(minResourcesList)
}

// CalculatePodResource returns the total resources of a pod.
// Request values take precedence over limit values.
func CalculatePodResource(podSpec corev1.PodSpec) corev1.ResourceList {
	podResource := corev1.ResourceList{}
	for _, container := range podSpec.Containers {
		containerResource := container.Resources.Requests
		if containerResource == nil {
			containerResource = corev1.ResourceList{}
		}
		for name, quantity := range container.Resources.Limits {
			if _, ok := containerResource[name]; !ok {
				containerResource[name] = quantity
			}
		}
		for name, quantity := range containerResource {
			if totalQuantity, ok := podResource[name]; ok {
				totalQuantity.Add(quantity)
				podResource[name] = totalQuantity
			} else {
				podResource[name] = quantity
			}
		}
	}
	return podResource
}

func ConvertResourceListToMapString(resourceList corev1.ResourceList) map[string]resource.Quantity {
	result := make(map[string]resource.Quantity)
	for key, value := range resourceList {
		result[string(key)] = value
	}
	return result
}

func sumResourceList(list []corev1.ResourceList) corev1.ResourceList {
	totalResource := corev1.ResourceList{}
	for _, l := range list {
		for name, quantity := range l {
			if value, ok := totalResource[name]; !ok {
				totalResource[name] = quantity.DeepCopy()
			} else {
				value.Add(quantity)
				totalResource[name] = value
			}
		}
	}
	return totalResource
}

func Contains(elems []string, searchTerm string) bool {
	for _, s := range elems {
		if searchTerm == s {
			return true
		}
	}
	return false
}

// GetHeadGroupServiceAccountName returns the head group service account if it exists.
// Otherwise, it returns the name of the cluster itself.
func GetHeadGroupServiceAccountName(cluster *rayv1.RayCluster) string {
	headGroupServiceAccountName := cluster.Spec.HeadGroupSpec.Template.Spec.ServiceAccountName
	if headGroupServiceAccountName != "" {
		return headGroupServiceAccountName
	}
	return cluster.Name
}

// CheckAllPodsRunning returns true if all the RayCluster's Pods are running, false otherwise
func CheckAllPodsRunning(ctx context.Context, runningPods corev1.PodList) bool {
	log := ctrl.LoggerFrom(ctx)
	// check if there are no pods.
	if len(runningPods.Items) == 0 {
		return false
	}
	for _, pod := range runningPods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			log.Info(fmt.Sprintf("CheckAllPodsRunning: Pod is not running; Pod Name: %s; Pod Status.Phase: %v", pod.Name, pod.Status.Phase))
			return false
		}
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status != corev1.ConditionTrue {
				log.Info(fmt.Sprintf("CheckAllPodsRunning: Pod is not ready; Pod Name: %s; Pod Status.Conditions[PodReady]: %v", pod.Name, cond))
				return false
			}
		}
	}
	return true
}

// CompareJsonStruct This is a way to better compare if two objects are the same when they are json/yaml structs. reflect.DeepEqual will fail in some cases.
func CompareJsonStruct(objA interface{}, objB interface{}) bool {
	a, err := json.Marshal(objA)
	if err != nil {
		return false
	}
	b, err := json.Marshal(objB)
	if err != nil {
		return false
	}
	var v1, v2 interface{}
	err = json.Unmarshal(a, &v1)
	if err != nil {
		return false
	}
	err = json.Unmarshal(b, &v2)
	if err != nil {
		return false
	}
	return reflect.DeepEqual(v1, v2)
}

// Json-serializes obj and returns its hash string
func GenerateJsonHash(obj interface{}) (string, error) {
	serialObj, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}

	hashBytes := sha1.Sum(serialObj) //nolint:gosec // We are not using this for security purposes

	// Convert to an ASCII string
	hashStr := base32.HexEncoding.EncodeToString(hashBytes[:])

	return hashStr, nil
}

// FindContainerPort searches for a specific port $portName in the container.
// If the port is found in the container, the corresponding port is returned.
// If the port is not found, the $defaultPort is returned instead.
func FindContainerPort(container *corev1.Container, portName string, defaultPort int) int {
	for _, port := range container.Ports {
		if port.Name == portName {
			return int(port.ContainerPort)
		}
	}
	return defaultPort
}

// IsJobFinished checks whether the given Job has finished execution.
// It does not discriminate between successful and failed terminations.
// src: https://github.com/kubernetes/kubernetes/blob/a8a1abc25cad87333840cd7d54be2efaf31a3177/pkg/controller/job/utils.go#L26
func IsJobFinished(j *batchv1.Job) (batchv1.JobConditionType, bool) {
	for _, c := range j.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			return c.Type, true
		}
	}
	return "", false
}

func EnvVarExists(envName string, envVars []corev1.EnvVar) bool {
	for _, env := range envVars {
		if env.Name == envName {
			return true
		}
	}
	return false
}

// EnvVarByName returns an entry in []corev1.EnvVar that matches a name.
// Also returns a bool for whether the env var exists.
func EnvVarByName(envName string, envVars []corev1.EnvVar) (corev1.EnvVar, bool) {
	for _, env := range envVars {
		if env.Name == envName {
			return env, true
		}
	}
	return corev1.EnvVar{}, false
}

type ClientProvider interface {
	GetDashboardClient(mgr manager.Manager) func() RayDashboardClientInterface
	GetHttpProxyClient(mgr manager.Manager) func() RayHttpProxyClientInterface
}
