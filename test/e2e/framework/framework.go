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

package framework

import (
	"context"

	"github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	"sigs.k8s.io/kueue/pkg/util/testing"
	//+kubebuilder:scaffold:imports
)

func CreateClientUsingCluster() client.Client {
	cfg := config.GetConfigOrDie()
	gomega.ExpectWithOffset(1, cfg).NotTo(gomega.BeNil())

	err := kueue.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	// +kubebuilder:scaffold:scheme
	client, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	return client
}

func KueueReadyForTesting(client client.Client) {
	// To verify that webhooks are ready, let's create a simple resourceflavor
	resourceKueue := testing.MakeResourceFlavor("default").Obj()
	gomega.Eventually(func() error {
		return client.Create(context.Background(), resourceKueue)
	}, Timeout, Interval).Should(gomega.Succeed())
	gomega.Expect(client.Delete(context.Background(), resourceKueue)).Should(gomega.Succeed())
}
