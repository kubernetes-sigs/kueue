/*
Copyright 2024 The Kubernetes Authors.

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

package multikueue

import (
	"context"
	"fmt"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/slices"
)

const (
	UsingKubeConfigs             = "spec.kubeconfigs"
	AdmissionCheckUsingConfigKey = "spec.multiKueueConfig"
)

var (
	configKind = "MultiKueueConfig"
	configGVK  = kueue.GroupVersion.WithKind(configKind)
)

func getIndexUsingKubeConfigs(configNamespace string) func(obj client.Object) []string {
	return func(obj client.Object) []string {
		cfg, isCfg := obj.(*kueuealpha.MultiKueueConfig)
		if !isCfg || len(cfg.Spec.Clusters) == 0 {
			return nil
		}
		return slices.Map(cfg.Spec.Clusters, func(c *kueuealpha.MultiKueueCluster) string {
			return strings.Join([]string{configNamespace, c.KubeconfigRef.Location}, "/")
		})
	}
}

func SetupIndexer(ctx context.Context, indexer client.FieldIndexer, configNamespace string) error {
	if err := indexer.IndexField(ctx, &kueuealpha.MultiKueueConfig{}, UsingKubeConfigs, getIndexUsingKubeConfigs(configNamespace)); err != nil {
		return fmt.Errorf("setting index on checks config used kubeconfig: %w", err)
	}
	if err := indexer.IndexField(ctx, &kueue.AdmissionCheck{}, AdmissionCheckUsingConfigKey, admissioncheck.IndexerByConfigFunction(ControllerName, configGVK)); err != nil {
		return fmt.Errorf("setting index on admission checks config: %w", err)
	}
	return nil
}
