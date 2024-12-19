/*
Copyright 2021 The Kubernetes Authors.

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

package webhooks

import (
	"context"
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

// +kubebuilder:webhook:path=/validate-localqueue,mutating=false,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=localqueues,verbs=create;update,versions=v1beta1,name=mlocalqueue.kb.io,admissionReviewVersions=v1

type localQueueValidator struct {
	Client  client.Client
	decoder admission.Decoder
}

// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=clusterqueuebindings,verbs=get;list;watch;create;update;patch;delete

func NewLocalQueueValidator(c client.Client, d admission.Decoder) admission.Handler {
	return &localQueueValidator{
		Client:  c,
		decoder: d,
	}
}

func (v *localQueueValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	// return admission.Allowed("LocalQueue is allowed to be created")
	lq := &kueue.LocalQueue{}

	err := v.decoder.Decode(req, lq)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	groups := req.UserInfo.Groups

	cqBindings := kueue.ClusterQueueBindingList{}
	cqs := kueue.ClusterQueueList{}

	err = v.Client.List(ctx, &cqs)
	if err != nil {
		return admission.Denied(err.Error())
	}
	err = v.Client.List(ctx, &cqBindings)
	if err != nil {
		return admission.Denied(err.Error())
	}
	for _, cqBinding := range cqBindings.Items {
		if cqBinding.Spec.ClusterQueue != lq.Spec.ClusterQueue {
			continue
		}
		for _, subject := range cqBinding.Spec.Subjects {
			if subject.Kind == "User" && subject.Name == req.UserInfo.Username {
				return admission.Allowed("User is allowed to create LocalQueue")
			} else {
				for _, group := range groups {
					if subject.Kind == "Group" && subject.Name == group {
						return admission.Allowed("Group is allowed to create LocalQueue")
					}
				}
			}
		}
	}
	return admission.Denied("User or Group is not allowed to create LocalQueue")
}

func (v *localQueueValidator) InjectDecoder(d admission.Decoder) error {
	v.decoder = d
	return nil
}
