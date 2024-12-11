package common

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
)

func RayClusterServeServiceNamespacedName(instance *rayv1.RayCluster) types.NamespacedName {
	return types.NamespacedName{
		Namespace: instance.Namespace,
		Name:      utils.GenerateServeServiceName(instance.Name),
	}
}

func RayClusterAutoscalerRoleNamespacedName(instance *rayv1.RayCluster) types.NamespacedName {
	return types.NamespacedName{Namespace: instance.Namespace, Name: instance.Name}
}

func RayClusterAutoscalerRoleBindingNamespacedName(instance *rayv1.RayCluster) types.NamespacedName {
	return types.NamespacedName{Namespace: instance.Namespace, Name: instance.Name}
}

func RayClusterAutoscalerServiceAccountNamespacedName(instance *rayv1.RayCluster) types.NamespacedName {
	return types.NamespacedName{Namespace: instance.Namespace, Name: utils.GetHeadGroupServiceAccountName(instance)}
}

func RayClusterHeadlessServiceListOptions(instance *rayv1.RayCluster) []client.ListOption {
	return []client.ListOption{
		client.InNamespace(instance.Namespace),
		client.MatchingLabels(map[string]string{utils.RayClusterHeadlessServiceLabelKey: instance.Name}),
	}
}

func RayClusterHeadServiceListOptions(instance *rayv1.RayCluster) []client.ListOption {
	return []client.ListOption{
		client.InNamespace(instance.Namespace),
		client.MatchingLabels(map[string]string{
			utils.RayClusterLabelKey:  instance.Name,
			utils.RayNodeTypeLabelKey: string(rayv1.HeadNode),
			utils.RayIDLabelKey:       utils.CheckLabel(utils.GenerateIdentifier(instance.Name, rayv1.HeadNode)),
		}),
	}
}

type AssociationOption interface {
	client.ListOption
	client.DeleteAllOfOption
}

type AssociationOptions []AssociationOption

func (list AssociationOptions) ToListOptions() (options []client.ListOption) {
	for _, option := range list {
		options = append(options, option.(client.ListOption))
	}
	return options
}

func (list AssociationOptions) ToDeleteOptions() (options []client.DeleteAllOfOption) {
	for _, option := range list {
		options = append(options, option.(client.DeleteAllOfOption))
	}
	return options
}

func (list AssociationOptions) ToMetaV1ListOptions() (options metav1.ListOptions) {
	listOptions := client.ListOptions{}
	for _, option := range list {
		option.(client.ListOption).ApplyToList(&listOptions)
	}
	return *listOptions.AsListOptions()
}

func RayClusterHeadPodsAssociationOptions(instance *rayv1.RayCluster) AssociationOptions {
	return AssociationOptions{
		client.InNamespace(instance.Namespace),
		client.MatchingLabels{
			utils.RayClusterLabelKey:  instance.Name,
			utils.RayNodeTypeLabelKey: string(rayv1.HeadNode),
		},
	}
}

func RayClusterWorkerPodsAssociationOptions(instance *rayv1.RayCluster) AssociationOptions {
	return AssociationOptions{
		client.InNamespace(instance.Namespace),
		client.MatchingLabels{
			utils.RayClusterLabelKey:  instance.Name,
			utils.RayNodeTypeLabelKey: string(rayv1.WorkerNode),
		},
	}
}

func RayClusterGroupPodsAssociationOptions(instance *rayv1.RayCluster, group string) AssociationOptions {
	return AssociationOptions{
		client.InNamespace(instance.Namespace),
		client.MatchingLabels{
			utils.RayClusterLabelKey:   instance.Name,
			utils.RayNodeGroupLabelKey: group,
		},
	}
}

func RayClusterAllPodsAssociationOptions(instance *rayv1.RayCluster) AssociationOptions {
	return AssociationOptions{
		client.InNamespace(instance.Namespace),
		client.MatchingLabels{
			utils.RayClusterLabelKey: instance.Name,
		},
	}
}

func RayServiceRayClustersAssociationOptions(rayService *rayv1.RayService) AssociationOptions {
	return AssociationOptions{
		client.InNamespace(rayService.Namespace),
		client.MatchingLabels{
			utils.RayOriginatedFromCRNameLabelKey: rayService.Name,
			utils.RayOriginatedFromCRDLabelKey:    utils.RayOriginatedFromCRDLabelValue(utils.RayServiceCRD),
		},
	}
}

func RayServiceServeServiceNamespacedName(rayService *rayv1.RayService) types.NamespacedName {
	if rayService.Spec.ServeService != nil && rayService.Spec.ServeService.Name != "" {
		return types.NamespacedName{
			Namespace: rayService.Namespace, // We do not respect s.Spec.ServeService.Namespace intentionally. Ref: https://github.com/ray-project/kuberay/blob/f6b4c3126654d1ef42965abc781846624b8e5dfc/ray-operator/controllers/ray/common/service.go#L298
			Name:      rayService.Spec.ServeService.Name,
		}
	}
	return types.NamespacedName{
		Namespace: rayService.Namespace,
		Name:      utils.GenerateServeServiceName(rayService.Name),
	}
}

func RayServiceActiveRayClusterNamespacedName(rayService *rayv1.RayService) types.NamespacedName {
	return types.NamespacedName{Name: rayService.Status.ActiveServiceStatus.RayClusterName, Namespace: rayService.Namespace}
}

func RayServicePendingRayClusterNamespacedName(rayService *rayv1.RayService) types.NamespacedName {
	return types.NamespacedName{Name: rayService.Status.PendingServiceStatus.RayClusterName, Namespace: rayService.Namespace}
}

// RayJobK8sJobNamespacedName is the only place to associate the RayJob with the submitter Kubernetes Job.
func RayJobK8sJobNamespacedName(rayJob *rayv1.RayJob) types.NamespacedName {
	return types.NamespacedName{
		Namespace: rayJob.Namespace,
		Name:      rayJob.Name,
	}
}

func RayJobRayClusterNamespacedName(rayJob *rayv1.RayJob) types.NamespacedName {
	return types.NamespacedName{
		Name:      rayJob.Status.RayClusterName,
		Namespace: rayJob.Namespace,
	}
}

// GetRayClusterHeadPod gets a *corev1.Pod from a *rayv1.RayCluster. Note that it returns (nil, nil) in the case of no head pod exists.
func GetRayClusterHeadPod(ctx context.Context, reader client.Reader, instance *rayv1.RayCluster) (*corev1.Pod, error) {
	logger := ctrl.LoggerFrom(ctx)

	runtimePods := corev1.PodList{}
	filterLabels := RayClusterHeadPodsAssociationOptions(instance)
	if err := reader.List(ctx, &runtimePods, filterLabels.ToListOptions()...); err != nil {
		return nil, err
	}
	if len(runtimePods.Items) == 0 {
		logger.Info("Found 0 head pod", "filter labels", filterLabels)
		return nil, nil
	}
	if len(runtimePods.Items) > 1 {
		logger.Info("Found multiple head pods", "count", len(runtimePods.Items), "filter labels", filterLabels)
		return nil, fmt.Errorf("found multiple heads. filter labels %v", filterLabels)
	}
	return &runtimePods.Items[0], nil
}
