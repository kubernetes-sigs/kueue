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

package behavioral

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

type ClusterInfo struct {
	Name   string
	Client client.Client
	Ctx    context.Context
}

//revive:disable:context-as-argument

func DefaultClusterInfosForTests(
	ctx1 context.Context,
	client1 client.Client,
	ctx2 context.Context,
	client2 client.Client,
) []ClusterInfo {
	return []ClusterInfo{
		{
			Name:   "worker1",
			Client: client1,
			Ctx:    ctx1,
		},
		{
			Name:   "worker2",
			Client: client2,
			Ctx:    ctx2,
		},
	}
}

//revive:enable:context-as-argument

func GetClientForSelectedWorkerCluster(g gomega.Gomega, managerWl *kueue.Workload, clusters ...ClusterInfo) ClusterInfo {
	ginkgo.GinkgoHelper()

	clusterName := managerWl.Status.ClusterName
	g.Expect(clusterName).ToNot(gomega.BeNil())

	for _, cluster := range clusters {
		if cluster.Name == *clusterName {
			return cluster
		}
	}

	ginkgo.Fail("none of the supplied clusters was selected")
	return ClusterInfo{}
}
