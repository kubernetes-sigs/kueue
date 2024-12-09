package common

import (
	"context"
	"fmt"

	"github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
)

const IngressClassAnnotationKey = "kubernetes.io/ingress.class"

// BuildIngressForHeadService Builds the ingress for head service dashboard.
// This is used to expose dashboard for external traffic.
func BuildIngressForHeadService(ctx context.Context, cluster rayv1.RayCluster) (*networkingv1.Ingress, error) {
	log := ctrl.LoggerFrom(ctx)

	labels := map[string]string{
		utils.RayClusterLabelKey:                cluster.Name,
		utils.RayIDLabelKey:                     utils.GenerateIdentifier(cluster.Name, rayv1.HeadNode),
		utils.KubernetesApplicationNameLabelKey: utils.ApplicationName,
		utils.KubernetesCreatedByLabelKey:       utils.ComponentName,
	}

	// Copy other ingress configurations from cluster annotations to provide a generic way
	// for user to customize their ingress settings. The `excludeSet` is used to avoid setting
	// both IngressClassAnnotationKey annotation which is deprecated and `Spec.IngressClassName`
	// at the same time.
	excludeSet := map[string]struct{}{
		IngressClassAnnotationKey: {},
	}
	annotation := map[string]string{}
	for key, value := range cluster.Annotations {
		if _, ok := excludeSet[key]; !ok {
			annotation[key] = value
		}
	}

	var paths []networkingv1.HTTPIngressPath
	pathType := networkingv1.PathTypeExact
	servicePorts := getServicePorts(cluster)
	dashboardPort := int32(utils.DefaultDashboardPort)
	if port, ok := servicePorts["dashboard"]; ok {
		dashboardPort = port
	}

	headSvcName, err := utils.GenerateHeadServiceName(utils.RayClusterCRD, cluster.Spec, cluster.Name)
	if err != nil {
		return nil, err
	}
	paths = []networkingv1.HTTPIngressPath{
		{
			Path:     "/" + cluster.Name + "/(.*)",
			PathType: &pathType,
			Backend: networkingv1.IngressBackend{
				Service: &networkingv1.IngressServiceBackend{
					Name: headSvcName,
					Port: networkingv1.ServiceBackendPort{
						Number: dashboardPort,
					},
				},
			},
		},
	}

	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        utils.GenerateIngressName(cluster.Name),
			Namespace:   cluster.Namespace,
			Labels:      labels,
			Annotations: annotation,
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{
				{
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: paths,
						},
					},
				},
			},
		},
	}

	// Get ingress class name from rayCluster annotations. this is a required field to use ingress.
	ingressClassName, ok := cluster.Annotations[IngressClassAnnotationKey]
	if !ok {
		log.Info(fmt.Sprintf("ingress class annotation is not set for cluster %s/%s", cluster.Namespace, cluster.Name))
	} else {
		// TODO: in AWS EKS, set up IngressClassName will cause an error due to conflict with annotation.
		ingress.Spec.IngressClassName = &ingressClassName
	}

	return ingress, nil
}
