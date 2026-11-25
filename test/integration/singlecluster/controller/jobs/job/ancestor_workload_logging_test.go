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

package job

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Ancestor Workload lookup error logging", ginkgo.Label("job:batch", "area:jobs"), func() {
	var (
		ns               *corev1.Namespace
		failWorkloadList atomic.Bool
		errWorkloadList  = errors.New("injected workload list failure")
	)

	newListFailingClient := func(config *rest.Config, options client.Options) (client.Client, error) {
		baseClient, err := client.NewWithWatch(config, options)
		if err != nil {
			return nil, err
		}
		return interceptor.NewClient(baseClient, interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*kueue.WorkloadList); ok && failWorkloadList.Load() {
					return errWorkloadList
				}
				return c.List(ctx, list, opts...)
			},
		}), nil
	}

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.SkipChildJobSuspension, false)
		failWorkloadList.Store(false)
		fwk.StartManagerForSpec(ctx, cfg,
			managerSetup(),
			framework.WithNewClient(newListFailingClient))
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ancestor-log-")
	})

	ginkgo.AfterEach(func() {
		failWorkloadList.Store(false)
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("logs one Reconciler error with the ancestorJob field per failed reconcile", func() {
		parentJob := testingjob.MakeJob("parent", ns.Name).Queue("test-lq").Obj()
		ginkgo.By("creating the parent job", func() {
			behavioral.MustCreate(ctx, k8sClient, parentJob)
		})

		ginkgo.By("waiting for the parent workload to be created", func() {
			parentWl := &kueue.Workload{}
			parentWlKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(parentJob.Name, parentJob.UID),
				Namespace: ns.Name,
			}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, parentWlKey, parentWl)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("making Workload list calls fail", func() {
			failWorkloadList.Store(true)
		})

		childJob := testingjob.MakeJob("child", ns.Name).Suspend(false).Obj()
		ginkgo.By("creating the child job", func() {
			gomega.Expect(ctrl.SetControllerReference(parentJob, childJob, k8sClient.Scheme())).To(gomega.Succeed())
			behavioral.MustCreate(ctx, k8sClient, childJob)
		})

		isChild := func(le observer.LoggedEntry) bool {
			m := le.ContextMap()
			return m["name"] == childJob.Name && m["namespace"] == ns.Name
		}

		var childErrors []observer.LoggedEntry
		ginkgo.By("waiting for an error record of the child job", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				childErrors = fwk.ObservedLogs.Filter(isChild).Filter(func(le observer.LoggedEntry) bool {
					return le.Level >= zapcore.ErrorLevel
				}).All()
				g.Expect(childErrors).NotTo(gomega.BeEmpty())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking each error record", func() {
			perReconcile := make(map[string]int)
			wantError := fmt.Sprintf("getting unfinished Workload for ancestor job %s/%s: %v", ns.Name, parentJob.Name, errWorkloadList)
			for _, le := range childErrors {
				m := le.ContextMap()
				gomega.Expect(le.Message).To(gomega.Equal("Reconciler error"))
				gomega.Expect(m).To(gomega.HaveKeyWithValue("ancestorJob", klog.KObj(parentJob).MarshalLog()))
				gomega.Expect(m).To(gomega.HaveKeyWithValue("error", wantError))
				gomega.Expect(m).To(gomega.HaveKeyWithValue("reconcileID", gomega.Not(gomega.BeEmpty())))
				gomega.Expect(m).NotTo(gomega.HaveKey("kueueErrorContext"))
				perReconcile[fmt.Sprint(m["reconcileID"])]++
			}
			for id, n := range perReconcile {
				gomega.Expect(n).To(gomega.Equal(1), "reconcileID %s logged %d error records", id, n)
			}
		})
	})
})
