/*
Copyright 2023 The Kubernetes Authors.

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

package deployment

import (
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/kueue/pkg/controller/constants"
)

// DeploymentWrapper wraps a Deployment.
type DeploymentWrapper struct {
	appsv1.Deployment
}

// MakeDeployment creates a wrapper for a Deployment with a single container.
func MakeDeployment(name, ns string) *DeploymentWrapper {
	return &DeploymentWrapper{appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: make(map[string]string, 1),
		},
		Spec: appsv1.DeploymentSpec{},
	}}
}

// Obj returns the inner Deployment.
func (d *DeploymentWrapper) Obj() *appsv1.Deployment {
	return &d.Deployment
}

// Clone returns deep copy of the Deployment.
func (d *DeploymentWrapper) Clone() *DeploymentWrapper {
	return &DeploymentWrapper{Deployment: *d.DeepCopy()}
}

// Label sets the label of the Deployment
func (d *DeploymentWrapper) Label(k, v string) *DeploymentWrapper {
	if d.Labels == nil {
		d.Labels = make(map[string]string)
	}
	d.Labels[k] = v
	return d
}

// Queue updates the queue name of the Deployment
func (d *DeploymentWrapper) Queue(q string) *DeploymentWrapper {
	return d.Label(constants.QueueLabel, q)
}

// Name updated the name of the Deployment
func (d *DeploymentWrapper) Name(n string) *DeploymentWrapper {
	d.ObjectMeta.Name = n
	return d
}
