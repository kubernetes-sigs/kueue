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

package multikueue

import (
	"context"
	"fmt"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
)

const (
	UsingKubeConfigs               = "spec.kubeconfigs"
	UsingClusterProfiles           = "spec.clusterProfiles"
	UsingMultiKueueClusters        = "spec.multiKueueClusters"
	AdmissionCheckUsingConfigKey   = "spec.multiKueueConfig"
	WorkloadsWithAdmissionCheckKey = "status.admissionChecks"
)

var (
	configGVK = kueue.GroupVersion.WithKind("MultiKueueConfig")
)

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

func SetupIndexer(ctx context.Context, indexer client.FieldIndexer, configNamespace string) error {
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
	if err := indexer.IndexField(ctx, &kueue.AdmissionCheck{}, AdmissionCheckUsingConfigKey, admissioncheck.IndexerByConfigFunction(kueue.MultiKueueControllerName, configGVK)); err != nil {
		return fmt.Errorf("setting index on admission checks config: %w", err)
	}
	return nil
}
