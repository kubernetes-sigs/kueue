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
	"os"
	"os/exec"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

// IsE2EModeDev returns true when E2E_MODE is set to "dev".
// Use this to skip teardown steps that only make sense when the cluster
// is kept around after the test run (local development), but are
// unnecessary in CI where the cluster is ephemeral.
func IsE2EModeDev() bool {
	return os.Getenv("E2E_MODE") == "dev"
}

func GetKueueNamespace() string {
	if ns := os.Getenv("KUEUE_NAMESPACE"); ns != "" {
		return ns
	}
	return configapi.DefaultNamespace
}

func GetKueueConfiguration(ctx context.Context, k8sClient client.Client) *configapi.Configuration {
	var kueueCfg configapi.Configuration
	kueueNS := GetKueueNamespace()
	kcmKey := types.NamespacedName{Namespace: kueueNS, Name: "kueue-manager-config"}
	configMap := &corev1.ConfigMap{}

	gomega.Expect(k8sClient.Get(ctx, kcmKey, configMap)).To(gomega.Succeed())
	gomega.Expect(yaml.Unmarshal([]byte(configMap.Data["controller_manager_config.yaml"]), &kueueCfg)).To(gomega.Succeed())
	return &kueueCfg
}

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
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}

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

func UpdateKueueConfigurationAndRestart(ctx context.Context, k8sClient client.Client, config *configapi.Configuration, kindClusterName string, applyChanges ...func(cfg *configapi.Configuration)) {
	ginkgo.GinkgoHelper()
	UpdateKueueConfiguration(ctx, k8sClient, config, applyChanges...)
	RestartKueueController(ctx, k8sClient, kindClusterName)
}

func GetClusterServerAddress(clusterName string) string {
	return "https://" + clusterName + "-control-plane:6443"
}

func GetKubernetesVersion(cfg *rest.Config) string {
	ginkgo.GinkgoHelper()
	discoveryClient := discovery.NewDiscoveryClientForConfigOrDie(cfg)
	ver, err := discoveryClient.ServerVersion()

	gomega.Expect(err).To(gomega.Succeed())
	return ver.String()
}

func exportKindLogs(ctx context.Context, kindClusterName string) {
	// Path to the kind binary
	kind := os.Getenv("KIND")
	// Path to the artifacts
	artifacts := os.Getenv("ARTIFACTS")

	if kind != "" && artifacts != "" {
		cmd := exec.CommandContext(ctx, kind, "export", "logs", "-n", kindClusterName, artifacts)
		cmd.Stdout = ginkgo.GinkgoWriter
		cmd.Stderr = ginkgo.GinkgoWriter
		gomega.Expect(cmd.Run()).To(gomega.Succeed())
	}
}
