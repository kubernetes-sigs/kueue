// TODO: Remove this file once Gateway API support is mature and Route-based dashboard access
// is no longer needed. OpenShift-specific resource construction should eventually be removed
// entirely. See: https://github.com/ray-project/kuberay/pull/4365#issuecomment-4143407845
package common

import (
	"maps"

	routev1 "github.com/openshift/api/route/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
)

// BuildRouteForHeadService Builds the Route (OpenShift) for head service dashboard.
// This is used to expose dashboard and remote submit service apis or external traffic.
func BuildRouteForHeadService(cluster rayv1.RayCluster) (*routev1.Route, error) {
	labels := map[string]string{
		utils.RayClusterLabelKey:                cluster.Name,
		utils.RayIDLabelKey:                     utils.CheckLabel(utils.GenerateIdentifier(cluster.Name, rayv1.HeadNode)),
		utils.KubernetesApplicationNameLabelKey: utils.ApplicationName,
		utils.KubernetesCreatedByLabelKey:       utils.ComponentName,
	}

	// Copy other configurations from cluster annotations to provide a generic way
	// for user to customize their route settings.
	annotation := map[string]string{}
	maps.Copy(annotation, cluster.Annotations)

	servicePorts := getServicePorts(cluster)
	dashboardPort := utils.DefaultDashboardPort
	if port, ok := servicePorts["dashboard"]; ok {
		dashboardPort = int(port)
	}

	weight := int32(100)

	serviceName, err := utils.GenerateHeadServiceName("RayCluster", cluster.Spec, cluster.Name)
	if err != nil {
		return nil, err
	}

	route := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:        utils.GenerateRouteName(cluster.Name),
			Namespace:   cluster.Namespace,
			Labels:      labels,
			Annotations: annotation,
		},
		Spec: routev1.RouteSpec{
			To: routev1.RouteTargetReference{
				Kind:   "Service",
				Name:   serviceName,
				Weight: &weight,
			},
			Port: &routev1.RoutePort{
				TargetPort: intstr.FromInt(dashboardPort),
			},
			WildcardPolicy: "None",
		},
	}

	return route, nil
}
