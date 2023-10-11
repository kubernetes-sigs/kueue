package podtemplate

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"sigs.k8s.io/controller-runtime/pkg/client"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
)

func ExtractByWorkloadLabel(ctx context.Context, c client.Client, w *kueue.Workload) (map[string]*corev1.PodTemplateSpec, error) {
	labelSelector := metav1.LabelSelector{MatchLabels: map[string]string{constants.WorkloadNameLabel: w.Name}}
	opt := &client.ListOptions{LabelSelector: labels.Set(labelSelector.MatchLabels).AsSelector(), Namespace: w.Namespace}
	return Extract(ctx, c, opt)
}

func Extract(ctx context.Context, client client.Client, opts ...client.ListOption) (map[string]*corev1.PodTemplateSpec, error) {
	var pts corev1.PodTemplateList
	if err := client.List(ctx, &pts, opts...); err != nil {
		return nil, err
	}
	return toMap(pts), nil
}

func toMap(pts corev1.PodTemplateList) map[string]*corev1.PodTemplateSpec {
	podTemplateSpec := make(map[string]*corev1.PodTemplateSpec)
	for _, pt := range pts.Items {
		ptNew := pt
		podTemplateSpec[pt.Name] = &ptNew.Template
	}
	return podTemplateSpec
}
