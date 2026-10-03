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

package e2e

import (
	"context"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
)

// GetKueueConfiguration retrieves the current Kueue configuration from ConfigMap
func GetKueueConfiguration(ctx context.Context, k8sClient client.Client) *configapi.Configuration {
	var kueueCfg configapi.Configuration
	kueueNS := GetKueueNamespace()
	kcmKey := types.NamespacedName{Namespace: kueueNS, Name: "kueue-manager-config"}
	configMap := &corev1.ConfigMap{}

	gomega.Expect(k8sClient.Get(ctx, kcmKey, configMap)).To(gomega.Succeed())
	gomega.Expect(yaml.Unmarshal([]byte(configMap.Data["controller_manager_config.yaml"]), &kueueCfg)).To(gomega.Succeed())
	return &kueueCfg
}

// UpdateKueueConfiguration updates the Kueue configuration by applying changes to the ConfigMap
func UpdateKueueConfiguration(ctx context.Context, k8sClient client.Client, config *configapi.Configuration, applyChanges ...func(cfg *configapi.Configuration)) {
	ginkgo.GinkgoHelper()
	startTime := time.Now()
	config = config.DeepCopy()
	for _, applyChange := range applyChanges {
		applyChange(config)
	}
	applyKueueConfiguration(ctx, k8sClient, config)
	ginkgo.GinkgoLogr.Info("Kueue configuration updated", "took", time.Since(startTime))
}

// UpdateKueueConfigurationAndRestart updates Kueue configuration and restarts the controller
func UpdateKueueConfigurationAndRestart(ctx context.Context, k8sClient client.Client, config *configapi.Configuration, kindClusterName string, applyChanges ...func(cfg *configapi.Configuration)) {
	ginkgo.GinkgoHelper()
	UpdateKueueConfiguration(ctx, k8sClient, config, applyChanges...)
	RestartKueueController(ctx, k8sClient, kindClusterName)
}

// applyKueueConfiguration applies configuration changes to the ConfigMap
func applyKueueConfiguration(ctx context.Context, k8sClient client.Client, kueueCfg *configapi.Configuration) {
	configMap := &corev1.ConfigMap{}
	kueueNS := GetKueueNamespace()
	kcmKey := types.NamespacedName{Namespace: kueueNS, Name: "kueue-manager-config"}
	config, err := yaml.Marshal(kueueCfg)

	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, kcmKey, configMap)).To(gomega.Succeed())
		configMap.Data["controller_manager_config.yaml"] = string(config)
		g.Expect(k8sClient.Update(ctx, configMap)).To(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())
}
