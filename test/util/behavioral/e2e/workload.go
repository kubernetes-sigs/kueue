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
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

func WorkloadKeyForLeaderWorkerSet(lws *leaderworkersetv1.LeaderWorkerSet, group string) client.ObjectKey {
	return types.NamespacedName{
		Name:      leaderworkerset.GetWorkloadName(lws.UID, lws.Name, group),
		Namespace: lws.Namespace,
	}
}

func ExpectWorkloadResourceUsage(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey, resourceName corev1.ResourceName, expected string) {
	ginkgo.GinkgoHelper()
	var wl kueue.Workload
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlKey, &wl)).To(gomega.Succeed())
		g.Expect(workload.HasQuotaReservation(&wl)).To(gomega.BeTrue())
		g.Expect(wl.Status.Admission).NotTo(gomega.BeNil())
		g.Expect(wl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))

		assignment := wl.Status.Admission.PodSetAssignments[0]
		g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(resourceName))
		usage := assignment.ResourceUsage[resourceName]
		g.Expect(usage.Cmp(resource.MustParse(expected))).To(gomega.Equal(0))
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed(), behavioral.AssertMsg("workload should have resource usage of "+expected+" for "+string(resourceName), &wl))
}

func ExpectWorkloadAdmittedWithCheck(ctx context.Context, wlLookupKey types.NamespacedName, acName, clusterName string, client client.Client) {
	ginkgo.GinkgoHelper()
	ginkgo.By(fmt.Sprintf("Waiting to be admitted in %s and manager clusters", clusterName))
	behavioral.ExpectWorkloadsToBeAdmittedByKeysWithTimeout(ctx, client, behavioral.MediumTimeout, wlLookupKey)
	behavioral.ExpectAdmissionCheckStateWithMessage(
		ctx, client, wlLookupKey,
		acName,
		kueue.CheckStateReady,
		fmt.Sprintf(`The workload was admitted on "%s"`, clusterName),
	)
}
