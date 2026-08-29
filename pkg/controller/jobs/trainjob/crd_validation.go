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

package trainjob

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ValidateTrainJobCRDVersion validates that the installed TrainJob CRD is v2.2.0 or later.
// It checks if the CRD schema supports the RuntimePatches field which was added in v2.2.0.
// Returns an error if the CRD is v2.1 or earlier, or if validation cannot be performed.
func ValidateTrainJobCRDVersion(ctx context.Context, c client.Client) error {
	crd := &unstructured.Unstructured{}
	crd.SetAPIVersion("apiextensions.k8s.io/v1")
	crd.SetKind("CustomResourceDefinition")

	crdKey := client.ObjectKey{Name: "trainjobs.trainer.kubeflow.org"}
	if err := c.Get(ctx, crdKey, crd); err != nil {
		return fmt.Errorf(
			"unable to fetch TrainJob CRD. "+
				"Ensure Kubeflow Trainer is installed and v2.2.0 or later. "+
				"Error: %w",
			err,
		)
	}

	// Check if the CRD schema has the runtimePatches field in TrainJobSpec
	// This field was added in Kubeflow Trainer v2.2.0
	if !hasRuntimePatchesField(crd) {
		return errors.New("TrainJob CRD does not support the 'runtimePatches' field. " +
			"This field is required by Kueue and was added in Kubeflow Trainer v2.2.0. " +
			"Currently installed TrainJob CRD appears to be v2.1 or earlier. " +
			"Please upgrade Kubeflow Trainer to v2.2.0 or later. " +
			"See: https://github.com/kubeflow/trainer/releases",
		)
	}

	return nil
}

// hasRuntimePatchesField checks if the CRD schema contains the runtimePatches field
// in the TrainJobSpec by inspecting the OpenAPI v3 schema definition.
func hasRuntimePatchesField(crd *unstructured.Unstructured) bool {
	// Navigate: spec.versions[0].schema.openAPIV3Schema.properties.spec.properties
	versions, found, err := unstructured.NestedSlice(crd.Object, "spec", "versions")
	if err != nil || !found || len(versions) == 0 {
		return false
	}

	// Get the first version (should be the served one, but we check first non-empty)
	for _, v := range versions {
		versionMap, ok := v.(map[string]any)
		if !ok {
			continue
		}

		// Navigate to the schema properties
		schemaProps, found, err := unstructured.NestedMap(
			versionMap,
			"schema", "openAPIV3Schema", "properties", "spec", "properties",
		)
		if err != nil || !found {
			continue
		}

		// Check if runtimePatches exists in spec.properties
		if _, exists := schemaProps["runtimePatches"]; exists {
			return true // v2.2.0+ detected
		}
	}

	return false
}
