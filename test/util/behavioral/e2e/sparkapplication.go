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

	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	sparkcommon "github.com/kubeflow/spark-operator/v2/pkg/common"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/test/util/behavioral"
)

// ExpectSparkApplicationRunningExecutorsAtLeast waits until the SparkApplication has at least
// wantRunning running, ungated executor Pods.
func ExpectSparkApplicationRunningExecutorsAtLeast(ctx context.Context, c client.Client, sparkAppKey client.ObjectKey, wantRunning int) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		pods := &corev1.PodList{}
		g.Expect(c.List(ctx, pods, client.InNamespace(sparkAppKey.Namespace), client.MatchingLabels{
			sparkcommon.LabelSparkAppName: sparkAppKey.Name,
			sparkcommon.LabelSparkRole:    sparkcommon.SparkRoleExecutor,
		})).To(gomega.Succeed())
		g.Expect(len(getRunningPods(pods.Items))).To(gomega.BeNumerically(">=", wantRunning), "running executor Pods")
	}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
}

// ExpectSparkApplicationToComplete waits until the SparkApplication is completed and returns it.
func ExpectSparkApplicationToComplete(ctx context.Context, c client.Client, sparkAppKey client.ObjectKey) *sparkv1beta2.SparkApplication {
	ginkgo.GinkgoHelper()
	sparkApp := &sparkv1beta2.SparkApplication{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, sparkAppKey, sparkApp)).To(gomega.Succeed())
		g.Expect(sparkApp.Status.AppState.State).To(gomega.Equal(sparkv1beta2.ApplicationStateCompleted))
	}, behavioral.VeryLongTimeout, behavioral.Interval).Should(gomega.Succeed(), behavioral.AssertMsg("SparkApplication did not complete", sparkApp))
	return sparkApp
}
