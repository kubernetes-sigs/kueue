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
	"errors"
	"fmt"

	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	kftrainerapi "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	testutil "sigs.k8s.io/kueue/test/util"
)

// DeleteNamespace deletes all objects the tests typically create in the namespace
func DeleteNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	if ns == nil {
		return nil
	}
	if err := DeleteAllAppWrappersInNamespace(ctx, c, ns); err != nil {
		return err
	}
	if err := DeleteAllJobSetsInNamespace(ctx, c, ns); err != nil {
		return err
	}
	if err := DeleteAllJobsInNamespace(ctx, c, ns); err != nil {
		return err
	}
	if err := DeleteAllRayJobsInNamespace(ctx, c, ns); err != nil {
		return err
	}
	if err := DeleteAllTrainingRuntimesInNamespace(ctx, c, ns); err != nil {
		return err
	}
	if err := DeleteAllMPIJobsInNamespace(ctx, c, ns); err != nil {
		return err
	}
	if err := c.DeleteAllOf(ctx, &appsv1.StatefulSet{}, client.InNamespace(ns.Name)); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err := c.DeleteAllOf(ctx, &kueue.LocalQueue{}, client.InNamespace(ns.Name)); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err := c.DeleteAllOf(ctx, &corev1.ConfigMap{}, client.InNamespace(ns.Name)); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err := deleteAllPodsInNamespace(ctx, c, ns, 2); err != nil {
		return err
	}
	if err := deleteWorkloadsInNamespace(ctx, c, ns, 2); err != nil {
		return err
	}
	if err := DeleteAllEventsInNamespace(ctx, c, ns); err != nil {
		return err
	}
	err := c.DeleteAllOf(ctx, &corev1.LimitRange{}, client.InNamespace(ns.Name), client.PropagationPolicy(metav1.DeletePropagationBackground))
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err := c.Delete(ctx, ns); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// DeleteAllCronJobsInNamespace deletes all CronJobs in namespace
func DeleteAllCronJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &batchv1.CronJob{})
}

// DeleteAllJobsInNamespace deletes all Jobs in namespace
func DeleteAllJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &batchv1.Job{})
}

// DeleteAllJobSetsInNamespace deletes all JobSets in namespace
func DeleteAllJobSetsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &jobset.JobSet{})
}

// DeleteAllTrainJobsInNamespace deletes all TrainJobs in namespace
func DeleteAllTrainJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &kftrainerapi.TrainJob{})
}

// DeleteAllTrainingRuntimesInNamespace deletes all TrainingRuntimes in namespace
func DeleteAllTrainingRuntimesInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &kftrainerapi.TrainingRuntime{})
}

// DeleteAllLeaderWorkerSetsInNamespace deletes all LeaderWorkerSets in namespace
func DeleteAllLeaderWorkerSetsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &leaderworkersetv1.LeaderWorkerSet{})
}

// DeleteAllMPIJobsInNamespace deletes all MPIJobs in namespace
func DeleteAllMPIJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &kfmpi.MPIJob{})
}

// DeleteAllPyTorchJobsInNamespace deletes all PyTorchJobs in namespace
func DeleteAllPyTorchJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &kftraining.PyTorchJob{})
}

// DeleteAllJAXJobsInNamespace deletes all JAXJobs in namespace
func DeleteAllJAXJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &kftraining.JAXJob{})
}

// DeleteAllAppWrappersInNamespace deletes all AppWrappers in namespace
func DeleteAllAppWrappersInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &awv1beta2.AppWrapper{})
}

// DeleteAllRayJobsInNamespace deletes all RayJobs in namespace
func DeleteAllRayJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &rayv1.RayJob{})
}

// DeleteAllRayServicesInNamespace deletes all RayServices in namespace
func DeleteAllRayServicesInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &rayv1.RayService{})
}

// DeleteAllSparkApplicationsInNamespace deletes all SparkApplications in namespace
func DeleteAllSparkApplicationsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &sparkv1beta2.SparkApplication{})
}

// DeleteAllPodsInNamespace deletes all Pods in namespace
func DeleteAllPodsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllPodsInNamespace(ctx, c, ns, 2)
}

// DeleteAllEventsInNamespace deletes all Events in namespace
func DeleteAllEventsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &eventsv1.Event{})
}

// ExpectAllPodsInNamespaceDeleted waits until all pods are deleted from namespace
func ExpectAllPodsInNamespaceDeleted(ctx context.Context, c client.Client, ns *corev1.Namespace) {
	ginkgo.GinkgoHelper()
	pods := corev1.PodList{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.List(ctx, &pods, client.InNamespace(ns.Name))).Should(gomega.Succeed())
		g.Expect(pods.Items).Should(gomega.BeEmpty())
	}, LongTimeout, Interval).Should(gomega.Succeed())
}

// DeleteWorkloadsInNamespace deletes all Workloads in namespace
func DeleteWorkloadsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteWorkloadsInNamespace(ctx, c, ns, 2)
}

// Helper functions

func deleteAllObjectsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace, obj client.Object) error {
	err := c.DeleteAllOf(ctx, obj, client.InNamespace(ns.Name), client.PropagationPolicy(metav1.DeletePropagationBackground))
	if err != nil && !apierrors.IsNotFound(err) && !errors.Is(err, &apimeta.NoKindMatchError{}) {
		return err
	}
	return nil
}

func deleteAllPodsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace, offset int) error {
	if err := client.IgnoreNotFound(c.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(ns.Name))); err != nil {
		return fmt.Errorf("deleting all Pods in namespace %q: %w", ns.Name, err)
	}
	pods := corev1.PodList{}
	gomega.EventuallyWithOffset(offset, func(g gomega.Gomega) {
		g.Expect(client.IgnoreNotFound(c.List(ctx, &pods, client.InNamespace(ns.Name)))).
			Should(gomega.Succeed(), "listing Pods with a finalizer in namespace %q", ns.Name)
		for _, p := range pods.Items {
			if controllerutil.RemoveFinalizer(&p, podconstants.PodFinalizer) {
				g.Expect(client.IgnoreNotFound(c.Update(ctx, &p))).Should(gomega.Succeed(), "removing finalizer")
			}
		}
	}, MediumTimeout, Interval).Should(gomega.Succeed(), testutil.AssertMsgObjList(fmt.Sprintf("Failed to clean up pods in namespace %q", ns.Name), &pods))
	return nil
}

func deleteWorkloadsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace, offset int) error {
	if err := c.DeleteAllOf(ctx, &kueue.Workload{}, client.InNamespace(ns.Name)); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	workloads := kueue.WorkloadList{}
	gomega.EventuallyWithOffset(offset, func(g gomega.Gomega) {
		g.Expect(c.List(ctx, &workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
		for _, wl := range workloads.Items {
			if controllerutil.RemoveFinalizer(&wl, kueue.ResourceInUseFinalizerName) {
				g.Expect(client.IgnoreNotFound(c.Update(ctx, &wl))).Should(gomega.Succeed())
			}
		}
	}, MediumTimeout, Interval).Should(gomega.Succeed(), testutil.AssertMsgObjList(fmt.Sprintf("Failed to clean up workloads in namespace %s", ns.Name), &workloads))
	return nil
}
