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

package core

import (
	"context"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/test/util"
)

type deletingConfigMapAdapter struct{}

func (*deletingConfigMapAdapter) SyncJob(context.Context, client.Client, client.Client, types.NamespacedName, string, string) (bool, error) {
	return false, nil
}

func (*deletingConfigMapAdapter) DeleteRemoteObject(ctx context.Context, _ client.Client, remoteClient client.Client, key types.NamespacedName) error {
	return remoteClient.Delete(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}})
}

func (*deletingConfigMapAdapter) IsJobManagedByKueue(context.Context, client.Client, types.NamespacedName) (bool, string, error) {
	return true, "", nil
}

func (*deletingConfigMapAdapter) GVK() schema.GroupVersionKind {
	return corev1.SchemeGroupVersion.WithKind("ConfigMap")
}

var _ = ginkgo.Describe("MultiKueue remote object identity", ginkgo.Label("area:multikueue", "feature:multikueue"), func() {
	var namespace *corev1.Namespace

	ginkgo.BeforeEach(func() {
		namespace = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "multikueue-identity-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, namespace)).To(gomega.Succeed())
	})

	ginkgo.It("preserves a replacement when deletion races after identity validation", func() {
		baseClient, err := client.NewWithWatch(cfg, client.Options{Scheme: k8sClient.Scheme(), Mapper: k8sClient.RESTMapper()})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		const (
			origin       = "manager-origin"
			workloadName = "manager-workload"
			managerUID   = "manager-job-uid"
		)
		key := types.NamespacedName{Name: "delete-race", Namespace: namespace.Name}
		makeConfigMap := func() *corev1.ConfigMap {
			return &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      key.Name,
					Namespace: key.Namespace,
					Labels: map[string]string{
						kueue.MultiKueueOriginLabel:               origin,
						controllerconstants.PrebuiltWorkloadLabel: workloadName,
					},
					Annotations: map[string]string{kueue.MultiKueueOriginUIDAnnotation: managerUID},
				},
			}
		}

		original := makeConfigMap()
		gomega.Expect(baseClient.Create(ctx, original)).To(gomega.Succeed())
		originalUID := original.UID
		gomega.Expect(originalUID).NotTo(gomega.BeEmpty())

		replaced := false
		racingClient := interceptor.NewClient(baseClient, interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if client.ObjectKeyFromObject(obj) != key || replaced {
					return c.Delete(ctx, obj, opts...)
				}
				replaced = true
				current := &corev1.ConfigMap{}
				if err := c.Get(ctx, key, current); err != nil {
					return err
				}
				if err := c.Delete(ctx, current, client.GracePeriodSeconds(0)); err != nil {
					return err
				}
				if err := wait.PollUntilContextTimeout(ctx, 10*time.Millisecond, 5*time.Second, true, func(ctx context.Context) (bool, error) {
					err := c.Get(ctx, key, &corev1.ConfigMap{})
					return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
				}); err != nil {
					return err
				}
				if err := c.Create(ctx, makeConfigMap()); err != nil {
					return err
				}
				return c.Delete(ctx, obj, opts...)
			},
		})

		err = jobframework.DeleteRemoteObjectWithCleanupContextIfOwned(
			ctx,
			baseClient,
			racingClient,
			&deletingConfigMapAdapter{},
			key,
			jobframework.MultiKueueRemoteObjectCleanupContext{
				RemoteObjectUID: originalUID,
				Association: jobframework.MultiKueueObjectAssociation{
					Origin:           origin,
					WorkloadName:     workloadName,
					ManagerObjectUID: managerUID,
				},
				WorkloadKey: types.NamespacedName{Name: workloadName, Namespace: key.Namespace},
			},
		)
		gomega.Expect(err).To(gomega.MatchError(apierrors.IsConflict, "API-server UID precondition conflict"))

		replacement := &corev1.ConfigMap{}
		gomega.Expect(baseClient.Get(ctx, key, replacement)).To(gomega.Succeed())
		gomega.Expect(replacement.UID).NotTo(gomega.Equal(originalUID))
	})
})
