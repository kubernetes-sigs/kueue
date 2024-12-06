package common

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
)

func getEnableRayHeadClusterIPService() bool {
	return strings.ToLower(os.Getenv(utils.ENABLE_RAY_HEAD_CLUSTER_IP_SERVICE)) == "true"
}

// HeadServiceLabels returns the default labels for a cluster's head service.
func HeadServiceLabels(cluster rayv1.RayCluster) map[string]string {
	return map[string]string{
		utils.RayClusterLabelKey:                cluster.Name,
		utils.RayNodeTypeLabelKey:               string(rayv1.HeadNode),
		utils.RayIDLabelKey:                     utils.CheckLabel(utils.GenerateIdentifier(cluster.Name, rayv1.HeadNode)),
		utils.KubernetesApplicationNameLabelKey: utils.ApplicationName,
		utils.KubernetesCreatedByLabelKey:       utils.ComponentName,
	}
}

// BuildServiceForHeadPod Builds the service for a pod. Currently, there is only one service that allows
// the worker nodes to connect to the head node.
func BuildServiceForHeadPod(ctx context.Context, cluster rayv1.RayCluster, labels map[string]string, annotations map[string]string) (*corev1.Service, error) {
	log := ctrl.LoggerFrom(ctx)

	if labels == nil {
		labels = make(map[string]string)
	}

	defaultLabels := HeadServiceLabels(cluster)

	// selector consists of *only* the keys in defaultLabels, updated with the values in labels if they exist
	selector := make(map[string]string)
	for k := range defaultLabels {
		if _, ok := labels[k]; ok {
			selector[k] = labels[k]
		} else {
			selector[k] = defaultLabels[k]
		}
	}

	// Deep copy the selector to avoid modifying the original object
	labelsForService := make(map[string]string)
	for k, v := range selector {
		labelsForService[k] = v
	}

	if annotations == nil {
		annotations = make(map[string]string)
	}

	defaultName, err := utils.GenerateHeadServiceName(utils.RayClusterCRD, cluster.Spec, cluster.Name)
	if err != nil {
		return nil, err
	}
	defaultNamespace := cluster.Namespace
	defaultType := cluster.Spec.HeadGroupSpec.ServiceType

	defaultAppProtocol := utils.DefaultServiceAppProtocol
	// `portsInt` is a map of port names to port numbers, while `ports` is a list of ServicePort objects
	portsInt := getServicePorts(cluster)
	ports := []corev1.ServicePort{}
	for name, port := range portsInt {
		svcPort := corev1.ServicePort{Name: name, Port: port, AppProtocol: &defaultAppProtocol}
		ports = append(ports, svcPort)
	}
	if cluster.Spec.HeadGroupSpec.HeadService != nil {
		// Use the provided "custom" HeadService.
		// Deep copy the HeadService to avoid modifying the original object
		headService := cluster.Spec.HeadGroupSpec.HeadService.DeepCopy()

		// For the selector, ignore any custom HeadService selectors or labels.
		headService.Spec.Selector = selector

		// Merge annotations with custom HeadService annotations. If there are overlaps,
		// ignore the custom HeadService annotations.
		if headService.ObjectMeta.Annotations == nil {
			headService.ObjectMeta.Annotations = make(map[string]string)
		}
		for k, v := range annotations {
			// if the key is present, log a warning message
			if _, ok := headService.ObjectMeta.Annotations[k]; ok {
				log.Info("Overwriting annotation provided in HeadGroupSpec.HeadService with value from HeadServiceAnnotations",
					"annotation_key", k,
					"headService_name", headService.ObjectMeta.Name,
					"annotation_value", v)
			}
			headService.ObjectMeta.Annotations[k] = v
		}

		// Append default ports.
		headService.Spec.Ports = append(headService.Spec.Ports, ports...)

		setLabelsforUserProvidedService(headService, labelsForService)
		setNameforUserProvidedService(ctx, headService, defaultName)
		setNamespaceforUserProvidedService(ctx, headService, defaultNamespace)
		setServiceTypeForUserProvidedService(ctx, headService, defaultType)

		return headService, nil
	}

	headService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        defaultName,
			Namespace:   defaultNamespace,
			Labels:      labelsForService,
			Annotations: annotations,
		},
		Spec: corev1.ServiceSpec{
			Selector: selector,
			Ports:    ports,
			Type:     defaultType,
		},
	}
	if !getEnableRayHeadClusterIPService() && (defaultType == "" || defaultType == corev1.ServiceTypeClusterIP) {
		// Make the head service headless by default, because a RayCluster should have at most one head Pod.
		headService.Spec.ClusterIP = corev1.ClusterIPNone
		headService.Spec.PublishNotReadyAddresses = true // We don't need to hide the Head address if its health checks failed.
	}

	// This change ensures that reconciliation in rayservice_controller will not update the Service spec due to change in ports order
	// sorting the ServicePorts on their name
	if len(headService.Spec.Ports) > 1 {
		sort.SliceStable(headService.Spec.Ports, func(i, j int) bool {
			return headService.Spec.Ports[i].Name < headService.Spec.Ports[j].Name
		})
	}

	return headService, nil
}

// BuildHeadServiceForRayService Builds the service for a pod. Currently, there is only one service that allows
// the worker nodes to connect to the head node.
// RayService controller updates the service whenever a new RayCluster serves the traffic.
func BuildHeadServiceForRayService(ctx context.Context, rayService rayv1.RayService, rayCluster rayv1.RayCluster) (*corev1.Service, error) {
	service, err := BuildServiceForHeadPod(ctx, rayCluster, nil, nil)
	if err != nil {
		return nil, err
	}

	headSvcName, err := utils.GenerateHeadServiceName(utils.RayServiceCRD, rayService.Spec.RayClusterSpec, rayService.Name)
	if err != nil {
		return nil, err
	}

	service.ObjectMeta.Name = headSvcName
	service.ObjectMeta.Namespace = rayService.Namespace
	service.ObjectMeta.Labels = map[string]string{
		utils.RayOriginatedFromCRNameLabelKey: rayService.Name,
		utils.RayOriginatedFromCRDLabelKey:    utils.RayOriginatedFromCRDLabelValue(utils.RayServiceCRD),
		utils.RayNodeTypeLabelKey:             string(rayv1.HeadNode),
		utils.RayIDLabelKey:                   utils.CheckLabel(utils.GenerateIdentifier(rayService.Name, rayv1.HeadNode)),
	}

	return service, nil
}

// BuildServeServiceForRayService builds the serve service for RayService.
func BuildServeServiceForRayService(ctx context.Context, rayService rayv1.RayService, rayCluster rayv1.RayCluster) (*corev1.Service, error) {
	return BuildServeService(ctx, rayService, rayCluster, true)
}

// BuildServeServiceForRayCluster builds the serve service for Ray cluster.
func BuildServeServiceForRayCluster(ctx context.Context, rayCluster rayv1.RayCluster) (*corev1.Service, error) {
	return BuildServeService(ctx, rayv1.RayService{}, rayCluster, false)
}

// BuildServeService builds the service for head node and worker nodes who have healthy http proxy to serve traffics.
func BuildServeService(ctx context.Context, rayService rayv1.RayService, rayCluster rayv1.RayCluster, isRayService bool) (*corev1.Service, error) {
	log := ctrl.LoggerFrom(ctx)
	name := rayCluster.Name
	namespace := rayCluster.Namespace
	crdType := utils.RayClusterCRD
	if isRayService {
		name = rayService.Name
		namespace = rayService.Namespace
		crdType = utils.RayServiceCRD
	}

	labels := map[string]string{
		utils.RayOriginatedFromCRNameLabelKey:  name,
		utils.RayOriginatedFromCRDLabelKey:     utils.RayOriginatedFromCRDLabelValue(crdType),
		utils.RayClusterServingServiceLabelKey: utils.GenerateServeServiceLabel(name),
	}

	selectorLabels := map[string]string{
		utils.RayClusterLabelKey: rayCluster.Name,
	}
	if isRayService {
		selectorLabels[utils.RayClusterServingServiceLabelKey] = utils.EnableRayClusterServingServiceTrue
	}

	defaultName := utils.GenerateServeServiceName(name)
	defaultNamespace := namespace
	defaultType := rayCluster.Spec.HeadGroupSpec.ServiceType
	if isRayService {
		defaultType = rayService.Spec.RayClusterSpec.HeadGroupSpec.ServiceType
	}

	// `portsInt` is a map of port names to port numbers, while `ports` is a list of ServicePort objects
	portsInt := getServicePorts(rayCluster)
	ports := []corev1.ServicePort{}
	for name, port := range portsInt {
		if name == utils.ServingPortName {
			svcPort := corev1.ServicePort{Name: name, Port: port}
			ports = append(ports, svcPort)
			break
		}
	}

	if isRayService {
		// We are invoked from RayService
		if len(ports) == 0 && rayService.Spec.ServeService == nil {
			return nil, fmt.Errorf("Please specify the port named 'serve' in the Ray head container; " +
				"otherwise, the Kubernetes service for Ray Serve will not be created.")
		}

		if rayService.Spec.ServeService != nil {
			// Use the provided "custom" ServeService.
			// Deep copy the ServeService to avoid modifying the original object
			serveService := rayService.Spec.ServeService.DeepCopy()

			// For the selector, ignore any custom ServeService selectors or labels.
			serveService.Spec.Selector = selectorLabels

			if serveService.ObjectMeta.Annotations == nil {
				serveService.ObjectMeta.Annotations = make(map[string]string)
			}

			// Add port with name "serve" if it is already not added and ignore any custom ports
			// Keeping this consistentent with adding only serve port in serve service
			if len(ports) != 0 {
				log.Info("port with name 'serve' already added. Ignoring user provided ports for serve service")
				serveService.Spec.Ports = ports
			} else {
				ports := []corev1.ServicePort{}
				for _, port := range serveService.Spec.Ports {
					if port.Name == utils.ServingPortName {
						svcPort := corev1.ServicePort{Name: port.Name, Port: port.Port}
						ports = append(ports, svcPort)
						break
					}
				}
				serveService.Spec.Ports = ports
			}

			setLabelsforUserProvidedService(serveService, labels)
			setNameforUserProvidedService(ctx, serveService, defaultName)
			setNamespaceforUserProvidedService(ctx, serveService, defaultNamespace)
			setServiceTypeForUserProvidedService(ctx, serveService, defaultType)

			return serveService, nil
		}
	}

	// We are invoked from cluster
	if len(ports) == 0 {
		return nil, fmt.Errorf("Please specify the port named 'serve' in the Ray head container; " +
			"otherwise, the Kubernetes service for Ray Serve will not be created.")
	}

	serveService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      defaultName,
			Namespace: defaultNamespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: selectorLabels,
			Ports:    ports,
			Type:     defaultType,
		},
	}

	return serveService, nil
}

// BuildHeadlessService builds the headless service for workers in multi-host worker groups to communicate
func BuildHeadlessServiceForRayCluster(rayCluster rayv1.RayCluster) *corev1.Service {
	name := rayCluster.Name + utils.DashSymbol + utils.HeadlessServiceSuffix
	namespace := rayCluster.Namespace

	labels := map[string]string{
		utils.RayClusterHeadlessServiceLabelKey: rayCluster.Name,
	}
	selectorLabels := map[string]string{
		utils.RayClusterLabelKey:  rayCluster.Name,
		utils.RayNodeTypeLabelKey: string(rayv1.WorkerNode),
	}

	headlessService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Selector:  selectorLabels,
			Type:      corev1.ServiceTypeClusterIP,
			// The headless worker service is used for peer communication between multi-host workers and should not be
			// dependent on Proxy Actor placement to publish DNS addresses.
			PublishNotReadyAddresses: true,
		},
	}

	return headlessService
}

func setServiceTypeForUserProvidedService(ctx context.Context, service *corev1.Service, defaultType corev1.ServiceType) {
	log := ctrl.LoggerFrom(ctx)
	// If the user has not specified a service type, use the default service type
	if service.Spec.Type == "" {
		log.Info("Using default serviceType passed for the user provided service",
			"default_type passed", defaultType,
			"service.ObjectMeta.Name", service.ObjectMeta.Name)
		service.Spec.Type = defaultType
	} else {
		log.Info("Overriding default serviceType with user provided serviceType",
			"default_type passed", defaultType,
			"service.ObjectMeta.Name", service.ObjectMeta.Name,
			"default_type passed", defaultType,
			"service.Spec.Type", service.Spec.Type)
	}
}

func setNamespaceforUserProvidedService(ctx context.Context, service *corev1.Service, defaultNamespace string) {
	log := ctrl.LoggerFrom(ctx)
	// If the user has specified a namespace, ignore it and raise a warning
	if service.ObjectMeta.Namespace != "" && service.ObjectMeta.Namespace != defaultNamespace {
		log.Info("Ignoring namespace in user provided service",
			"provided_namespace", service.ObjectMeta.Namespace,
			"service_name", service.ObjectMeta.Name,
			"default_namespace", defaultNamespace)
	}

	service.ObjectMeta.Namespace = defaultNamespace
}

func setNameforUserProvidedService(ctx context.Context, service *corev1.Service, defaultName string) {
	log := ctrl.LoggerFrom(ctx)
	// If the user has not specified a name, use the default name passed
	if service.ObjectMeta.Name == "" {
		log.Info("Using default name for user provided service.", "default_name", defaultName)
		service.ObjectMeta.Name = defaultName
	} else {
		log.Info("Overriding default name for user provided service with name in service.ObjectMeta.Name.",
			"default_name", defaultName,
			"provided_name", service.ObjectMeta.Name)
	}
}

func setLabelsforUserProvidedService(service *corev1.Service, labels map[string]string) {
	// For the Labels field, merge labels with user provided labels.
	// If there are overlaps, ignore the user provided Service labels.
	if service.ObjectMeta.Labels == nil {
		service.ObjectMeta.Labels = make(map[string]string)
	}
	for k, v := range labels {
		service.ObjectMeta.Labels[k] = v
	}
}

// getServicePorts will either user passing ports or default ports to create service.
func getServicePorts(cluster rayv1.RayCluster) map[string]int32 {
	ports := getPortsFromCluster(cluster)
	// Assign default ports
	if len(ports) == 0 {
		ports = getDefaultPorts()
	}

	// check if metrics port is defined. If not, add default value for it.
	if _, metricsDefined := ports[utils.MetricsPortName]; !metricsDefined {
		ports[utils.MetricsPortName] = utils.DefaultMetricsPort
	}

	return ports
}

// getPortsFromCluster get the ports from head container and directly map them in service
// It's user's responsibility to maintain rayStartParam ports and container ports mapping
// TODO: Consider to infer ports from rayStartParams (source of truth) in the future.
func getPortsFromCluster(cluster rayv1.RayCluster) map[string]int32 {
	svcPorts := map[string]int32{}

	cPorts := cluster.Spec.HeadGroupSpec.Template.Spec.Containers[utils.RayContainerIndex].Ports
	for _, port := range cPorts {
		if port.Name == "" {
			port.Name = fmt.Sprint(port.ContainerPort) + "-port"
		}
		svcPorts[port.Name] = port.ContainerPort
	}

	return svcPorts
}

func getDefaultPorts() map[string]int32 {
	return map[string]int32{
		utils.ClientPortName:    utils.DefaultClientPort,
		utils.RedisPortName:     utils.DefaultRedisPort,
		utils.DashboardPortName: utils.DefaultDashboardPort,
		utils.MetricsPortName:   utils.DefaultMetricsPort,
		utils.ServingPortName:   utils.DefaultServingPort,
	}
}
