/*
Copyright 2022 The Kubernetes Authors.

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

package testing

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func PodSpecForRequest(request map[corev1.ResourceName]string) corev1.PodSpec {
	return corev1.PodSpec{
		Containers: SingleContainerForRequest(request),
	}
}

func SingleContainerForRequest(request map[corev1.ResourceName]string) []corev1.Container {
	rl := make(corev1.ResourceList, len(request))
	for name, val := range request {
		rl[name] = resource.MustParse(val)
	}
	return []corev1.Container{
		{
			Resources: corev1.ResourceRequirements{
				Requests: rl,
			},
		},
	}
}

// CheckEventRecordedFor checks if an event identified by eventReason, eventType, eventNote
// was recorded for the object indentified by ref.
func CheckEventRecordedFor(ctx context.Context, k8sClient client.Client,
	eventReason string, eventType string, eventMessage string,
	ref types.NamespacedName) (bool, error) {
	events := &corev1.EventList{}
	if err := k8sClient.List(ctx, events, client.InNamespace(ref.Namespace)); err != nil {
		return false, err
	}

	for i := range events.Items {
		item := &events.Items[i]
		if item.InvolvedObject.Name == ref.Name && item.Reason == eventReason && item.Type == eventType && item.Message == eventMessage {
			return true, nil
		}
	}
	return false, fmt.Errorf("event not found after checking %d events, eventReason: %s , eventType: %s, eventMessage: %s, namespace: %s ",
		len(events.Items), eventReason, eventType, eventMessage, ref.Namespace)
}

// HasEventAppeared returns if an event has been emitted
func HasEventAppeared(ctx context.Context, k8sClient client.Client, event corev1.Event) (bool, error) {
	events := &corev1.EventList{}
	if err := k8sClient.List(ctx, events, &client.ListOptions{}); err != nil {
		return false, err
	}
	for _, item := range events.Items {
		if item.Reason == event.Reason && item.Type == event.Type && item.Message == event.Message {
			return true, nil
		}
	}
	return false, nil
}
