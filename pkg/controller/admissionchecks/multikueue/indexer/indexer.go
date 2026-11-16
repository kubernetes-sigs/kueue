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

// Package indexer holds the field indexes of the MultiKueue controllers, both in the
// multikueue package and in its sub-packages, which cannot import multikueue.
package indexer

import (
	"context"
	"fmt"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
)

const (
	UsingKubeConfigs                    = "spec.kubeconfigs"
	UsingClusterProfiles                = "spec.clusterProfiles"
	UsingMultiKueueClusters             = "spec.multiKueueClusters"
	AdmissionCheckUsingConfigKey        = "spec.multiKueueConfig"
	WorkloadsWithAdmissionCheckKey      = "status.admissionChecks"
	AdmissionCheckControllerNameKey     = "spec.controllerName"
	ClusterQueueAdmissionChecksKey      = "spec.admissionChecks"
	CapacityProviderParametersKey       = "spec.parameters.name"
	ProviderConfigManagerSubtreeRootKey = "spec.managerSubtreeRootRef.name"
)

var capacityProviderConfigGK = kueuealpha.SchemeGroupVersion.WithKind("MultiKueueCapacityProviderConfig").GroupKind()

func kubeConfigLocationIndexerFunc(configNamespace string) func(obj client.Object) []string {
	return func(obj client.Object) []string {
		cluster, isCluster := obj.(*kueue.MultiKueueCluster)
		if !isCluster || cluster.Spec.ClusterSource.KubeConfig == nil {
			return nil
		}
		return []string{strings.Join([]string{configNamespace, cluster.Spec.ClusterSource.KubeConfig.Location}, "/")}
	}
}

func clusterProfileRefIndexerFunc(configNamespace string) func(obj client.Object) []string {
	return func(obj client.Object) []string {
		cluster, isCluster := obj.(*kueue.MultiKueueCluster)
		if !isCluster {
			return nil
		}
		if cluster.Spec.ClusterSource.ClusterProfileRef == nil {
			return nil
		}
		return []string{strings.Join([]string{configNamespace, cluster.Spec.ClusterSource.ClusterProfileRef.Name}, "/")}
	}
}

func multiKueueClustersIndexerFunc(obj client.Object) []string {
	config, isConfig := obj.(*kueue.MultiKueueConfig)
	if !isConfig {
		return nil
	}
	return config.Spec.Clusters
}

func admissionCheckControllerNameIndexerFunc(obj client.Object) []string {
	ac, ok := obj.(*kueue.AdmissionCheck)
	if !ok {
		return nil
	}
	return []string{ac.Spec.ControllerName}
}

func clusterQueueAdmissionChecksIndexerFunc(obj client.Object) []string {
	cq, ok := obj.(*kueue.ClusterQueue)
	if !ok || cq.Spec.AdmissionChecksStrategy == nil {
		return nil
	}
	var names []string
	for _, rule := range cq.Spec.AdmissionChecksStrategy.AdmissionChecks {
		names = append(names, string(rule.Name))
	}
	return names
}

// capacityProviderParametersIndexerFunc indexes the MultiKueue CapacityProviders by the name of
// the MultiKueueCapacityProviderConfig in spec.parameters.
func capacityProviderParametersIndexerFunc(obj client.Object) []string {
	cp, ok := obj.(*kueuealpha.CapacityProvider)
	if !ok || cp.Spec.ControllerName != kueue.MultiKueueControllerName || cp.Spec.Parameters == nil ||
		cp.Spec.Parameters.APIGroup != capacityProviderConfigGK.Group || cp.Spec.Parameters.Kind != capacityProviderConfigGK.Kind {
		return nil
	}
	return []string{cp.Spec.Parameters.Name}
}

// providerConfigManagerSubtreeRootIndexerFunc indexes MultiKueueCapacityProviderConfigs by
// spec.managerSubtreeRootRef.name. In Alpha, only ClusterQueue is supported as a subtree root;
// Cohort support may be added in a future release.
func providerConfigManagerSubtreeRootIndexerFunc(obj client.Object) []string {
	cfg, ok := obj.(*kueuealpha.MultiKueueCapacityProviderConfig)
	if !ok || cfg.Spec.ManagerSubtreeRootRef.Kind != kueuealpha.ClusterQueueSubtreeRootRefKind {
		return nil
	}
	return []string{cfg.Spec.ManagerSubtreeRootRef.Name}
}

// Setup sets up the field indexes of the MultiKueue controllers. The indexes used only by the
// MultiKueue CapacityProvider controller are set up when the DQOMultiKueueCapacityProvider
// feature gate is enabled.
func Setup(ctx context.Context, indexer client.FieldIndexer, configNamespace string) error {
	if err := indexer.IndexField(ctx, &kueue.MultiKueueCluster{}, UsingKubeConfigs, kubeConfigLocationIndexerFunc(configNamespace)); err != nil {
		return fmt.Errorf("setting index on clusters using kubeconfig: %w", err)
	}
	if features.Enabled(features.MultiKueueClusterProfile) {
		if err := indexer.IndexField(ctx, &kueue.MultiKueueCluster{}, UsingClusterProfiles, clusterProfileRefIndexerFunc(configNamespace)); err != nil {
			return fmt.Errorf("setting index on clusters using cluster profiles: %w", err)
		}
	}
	if err := indexer.IndexField(ctx, &kueue.MultiKueueConfig{}, UsingMultiKueueClusters, multiKueueClustersIndexerFunc); err != nil {
		return fmt.Errorf("setting index on configs using clusters: %w", err)
	}
	configGVK := kueue.SchemeGroupVersion.WithKind("MultiKueueConfig")
	if err := indexer.IndexField(ctx, &kueue.AdmissionCheck{}, AdmissionCheckUsingConfigKey, admissioncheck.IndexerByConfigFunction(kueue.MultiKueueControllerName, configGVK)); err != nil {
		return fmt.Errorf("setting index on admission checks config: %w", err)
	}
	if features.Enabled(features.DQOMultiKueueCapacityProvider) {
		if err := indexer.IndexField(ctx, &kueue.AdmissionCheck{}, AdmissionCheckControllerNameKey, admissionCheckControllerNameIndexerFunc); err != nil {
			return fmt.Errorf("setting index on admission checks controllerName: %w", err)
		}
		if err := indexer.IndexField(ctx, &kueue.ClusterQueue{}, ClusterQueueAdmissionChecksKey, clusterQueueAdmissionChecksIndexerFunc); err != nil {
			return fmt.Errorf("setting index on cluster queues admissionChecks: %w", err)
		}
		if err := indexer.IndexField(ctx, &kueuealpha.CapacityProvider{}, CapacityProviderParametersKey, capacityProviderParametersIndexerFunc); err != nil {
			return fmt.Errorf("setting index on capacity providers parameters: %w", err)
		}
		if err := indexer.IndexField(ctx, &kueuealpha.MultiKueueCapacityProviderConfig{}, ProviderConfigManagerSubtreeRootKey, providerConfigManagerSubtreeRootIndexerFunc); err != nil {
			return fmt.Errorf("setting index on capacity provider configs managerSubtreeRootRef: %w", err)
		}
	}
	return nil
}
