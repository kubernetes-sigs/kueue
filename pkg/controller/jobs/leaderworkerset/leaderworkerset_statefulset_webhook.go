/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package leaderworkerset

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	v1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

type StatefulSetWebhook struct {
}

func SetupStatefulSetWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	wh := &StatefulSetWebhook{}
	return ctrl.NewWebhookManagedBy(mgr).
		For(&appsv1.StatefulSet{}).
		WithDefaulter(wh).
		WithLogConstructor(roletracker.WebhookLogConstructor(options.RoleTracker)).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-apps-v1-statefulset,mutating=true,failurePolicy=fail,sideEffects=None,groups="apps",resources=statefulsets,verbs=create,versions=v1,name=mstatefulset.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &StatefulSetWebhook{}

func (wh *StatefulSetWebhook) Default(ctx context.Context, obj runtime.Object) error {
	sts, ok := obj.(*appsv1.StatefulSet)
	if !ok {
		return nil
	}

	log := ctrl.LoggerFrom(ctx).WithName("leaderworkerset-statefulset-webhook")
	log.V(3).Info("Defaulting")

	if sts.UID == "" {
		fmt.Println("UID is empty")
	}
	fmt.Println(sts.Labels)
	fmt.Println(sts.Annotations)
	fmt.Println(sts.OwnerReferences)

	if sts.Annotations != nil {
		sts.Annotations[podconstants.SuspendedByParentAnnotation] = FrameworkName
		if _, ok := sts.Labels[v1.SetNameLabelKey]; !ok {
			if sts.Annotations == nil {
				sts.Annotations = map[string]string{}
			}
			sts.Annotations[podconstants.SuspendedByParentAnnotation] = FrameworkName
			log.V(3).Info("Set SuspendedByParentAnnotation", "parent", FrameworkName)
		}
	}

	log.V(3).Info("No NameLabelKey")

	return nil
}
