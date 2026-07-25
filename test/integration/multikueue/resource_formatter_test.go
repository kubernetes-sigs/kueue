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
	"time"

	"github.com/go-logr/logr"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	worker1CounterResource = corev1.ResourceName("example.com/worker1-memory")
	worker2CounterResource = corev1.ResourceName("example.com/worker2-memory")
)

var _ = ginkgo.Describe("MultiKueue worker resource formatting", ginkgo.Label("area:multikueue", "feature:multikueue", "feature:dra"), func() {
	ginkgo.It("keeps counter resource formatting isolated between worker managers", func() {
		worker1Quantities := workerResourceUsageStrings(worker1TestCluster, worker1CounterResource, worker2CounterResource)
		gomega.Expect(worker1Quantities).To(gomega.Equal(map[corev1.ResourceName]string{
			worker1CounterResource: "9984Mi",
			worker2CounterResource: "10468982784",
		}))

		worker2Quantities := workerResourceUsageStrings(worker2TestCluster, worker1CounterResource, worker2CounterResource)
		gomega.Expect(worker2Quantities).To(gomega.Equal(map[corev1.ResourceName]string{
			worker1CounterResource: "10468982784",
			worker2CounterResource: "9984Mi",
		}))
	})
})

func workerResourceUsageStrings(c cluster, resources ...corev1.ResourceName) map[corev1.ResourceName]string {
	const (
		clusterQueueName = "formatter-isolation"
		flavorName       = "formatter-isolation"
	)

	log := logr.Discard()
	resourceFlavor := utiltestingapi.MakeResourceFlavor(flavorName).Obj()
	c.schedulerCache.AddOrUpdateResourceFlavor(log, resourceFlavor)
	ginkgo.DeferCleanup(func() {
		c.schedulerCache.DeleteResourceFlavor(log, resourceFlavor)
	})

	flavorQuotas := utiltestingapi.MakeFlavorQuotas(flavorName)
	podSetAssignment := utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName)
	workloadBuilder := utiltestingapi.MakeWorkload("formatter-isolation", "")
	for _, resourceName := range resources {
		flavorQuotas.Resource(resourceName, "20Gi")
		podSetAssignment.Assignment(resourceName, flavorName, "9984Mi")
		workloadBuilder.Request(resourceName, "9984Mi")
	}

	clusterQueue := utiltestingapi.MakeClusterQueue(clusterQueueName).
		ResourceGroup(*flavorQuotas.Obj()).
		NamespaceSelector(nil).
		Obj()
	gomega.Expect(c.schedulerCache.AddClusterQueue(c.ctx, clusterQueue)).To(gomega.Succeed())
	ginkgo.DeferCleanup(func() {
		c.schedulerCache.DeleteClusterQueue(clusterQueue)
	})

	workerWorkload := workloadBuilder.ReserveQuotaAt(
		utiltestingapi.MakeAdmission(clusterQueueName).PodSets(podSetAssignment.Obj()).Obj(),
		time.Now(),
	).Obj()
	gomega.Expect(c.schedulerCache.AddOrUpdateWorkload(log, workerWorkload)).To(gomega.BeTrue())
	ginkgo.DeferCleanup(func() {
		gomega.Expect(c.schedulerCache.DeleteWorkload(log, workload.Key(workerWorkload))).To(gomega.Succeed())
	})

	usage, err := c.schedulerCache.Usage(clusterQueue)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	result := make(map[corev1.ResourceName]string, len(resources))
	for _, flavorUsage := range usage.ReservedResources {
		for _, resourceUsage := range flavorUsage.Resources {
			result[resourceUsage.Name] = resourceUsage.Total.String()
		}
	}
	return result
}
