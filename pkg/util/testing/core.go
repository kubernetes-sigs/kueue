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
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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

// CheckLatestEvent will return true if the latest event is as you want.
func CheckLatestEvent(ctx context.Context, k8sClient client.Client,
	eventReason string,
	eventType string, eventNote string) (bool, error) {
	events := &eventsv1.EventList{}
	if err := k8sClient.List(ctx, events, &client.ListOptions{}); err != nil {
		return false, err
	}

	length := len(events.Items)
	if length == 0 {
		return false, errors.New("no events currently exist")
	}

	item := events.Items[length-1]
	if item.Reason == eventReason && item.Type == eventType && item.Note == eventNote {
		return true, nil
	}

	return false, fmt.Errorf("mismatch with the latest event: got r:%v t:%v n:%v, reg %v", item.Reason, item.Type, item.Note, item.Regarding)
}

// HasEventAppeared return if an event has been emitted
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
