/*
Copyright The Kubeflow Authors.

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

package core

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
)

const (
	runtimeSnapshotSuffix = "-runtime-snapshot"
	runtimeDataKey        = "runtime"
)

// getRuntimeSnapshot retrieves the runtime snapshot from the ConfigMap and unmarshalls the value into runtimeObj.
// Returns an error if the snapshot ConfigMap doesn't exist or if the data is invalid. The value of runtimeObj is
// only valid if no error was returned.
func getRuntimeSnapshot(ctx context.Context, c client.Client, trainJob *trainer.TrainJob, runtimeObj client.Object) error {
	cm := &corev1.ConfigMap{}
	cmKey := client.ObjectKey{
		Name:      trainJob.Name + runtimeSnapshotSuffix,
		Namespace: trainJob.Namespace,
	}

	if err := c.Get(ctx, cmKey, cm); err != nil {
		return err
	}

	// Validate snapshot belongs to this TrainJob
	if owner := metav1.GetControllerOf(cm); owner == nil || owner.UID != trainJob.UID {
		return fmt.Errorf("invalid runtime snapshot: a snapshot configmap exists but is owned by another object")
	}

	// Read the runtime data from ConfigMap
	runtimeYAML, ok := cm.Data[runtimeDataKey]
	if !ok {
		return fmt.Errorf("invalid runtime snapshot: snapshot ConfigMap missing %q data key", runtimeDataKey)
	}

	// Unmarshal YAML into the runtime type
	if err := yaml.Unmarshal([]byte(runtimeYAML), runtimeObj); err != nil {
		return fmt.Errorf("invalid runtime snapshot: unable to unmarshal the snapshot: %w", err)
	}

	// Validate snapshot matches expected RuntimeRef
	snapshotGVK := runtimeObj.GetObjectKind().GroupVersionKind()
	kindMismatch := trainJob.Spec.RuntimeRef.Kind != nil && snapshotGVK.Kind != *trainJob.Spec.RuntimeRef.Kind
	groupMismatch := trainJob.Spec.RuntimeRef.APIGroup != nil && snapshotGVK.Group != *trainJob.Spec.RuntimeRef.APIGroup
	nameMismatch := runtimeObj.GetName() != trainJob.Spec.RuntimeRef.Name

	if kindMismatch || groupMismatch || nameMismatch {
		expectedKind := "<any>"
		if trainJob.Spec.RuntimeRef.Kind != nil {
			expectedKind = *trainJob.Spec.RuntimeRef.Kind
		}
		expectedGroup := "<any>"
		if trainJob.Spec.RuntimeRef.APIGroup != nil {
			expectedGroup = *trainJob.Spec.RuntimeRef.APIGroup
		}
		return fmt.Errorf(
			"invalid runtime snapshot: the snapshot refers to the wrong runtime: expecting a runtime with name, api group and kind of %q, %q, %q but found runtime with name, api group and kind of %q, %q, %q",
			trainJob.Spec.RuntimeRef.Name,
			expectedGroup,
			expectedKind,
			runtimeObj.GetName(),
			snapshotGVK.Group,
			snapshotGVK.Kind,
		)
	}

	return nil
}

// createRuntimeSnapshot creates a ConfigMap containing a YAML-serialized snapshot of the runtime configuration.
// The ConfigMap is owned by the TrainJob and will be automatically deleted when the TrainJob is deleted.
// An existing configmap will be overwritten. Callers must only invoke this when no snapshot exists.
func createRuntimeSnapshot(ctx context.Context, c client.Client, trainJob *trainer.TrainJob, runtimeObj client.Object) error {
	// Serialize the runtime object to YAML
	runtimeYAML, err := yaml.Marshal(runtimeObj)
	if err != nil {
		return fmt.Errorf("marshaling runtime to YAML: %w", err)
	}

	// Create ConfigMap ApplyConfiguration with runtime snapshot
	cmAC := corev1ac.ConfigMap(trainJob.Name+runtimeSnapshotSuffix, trainJob.Namespace).
		WithOwnerReferences(
			metav1ac.OwnerReference().
				WithAPIVersion(trainer.GroupVersion.String()).
				WithKind(trainer.TrainJobKind).
				WithName(trainJob.Name).
				WithUID(trainJob.UID).
				WithController(true).
				WithBlockOwnerDeletion(true),
		).
		WithData(map[string]string{
			runtimeDataKey: string(runtimeYAML),
		})

	if err := c.Apply(ctx, cmAC, client.FieldOwner("trainer"), client.ForceOwnership); err != nil {
		return fmt.Errorf("applying runtime snapshot ConfigMap: %w", err)
	}

	return nil
}
