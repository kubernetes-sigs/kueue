package util

import (
	"context"
	"fmt"
	"os"

	"github.com/google/go-cmp/cmp/cmpopts"
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	visibility "sigs.k8s.io/kueue/apis/visibility/v1beta1"
	kueueclientset "sigs.k8s.io/kueue/client-go/clientset/versioned"
	visibilityv1beta1 "sigs.k8s.io/kueue/client-go/clientset/versioned/typed/visibility/v1beta1"
)

const (
	// E2eTestSleepImageOld is the image used for testing rolling update.
	E2eTestSleepImageOld = "gcr.io/k8s-staging-perf-tests/sleep:v0.0.3@sha256:00ae8e01dd4439edfb7eb9f1960ac28eba16e952956320cce7f2ac08e3446e6b"
	// E2eTestSleepImage is the image used for testing.
	E2eTestSleepImage = "gcr.io/k8s-staging-perf-tests/sleep:v0.1.0@sha256:8d91ddf9f145b66475efda1a1b52269be542292891b5de2a7fad944052bab6ea"
)

func CreateClientUsingCluster(kContext string) (client.WithWatch, *rest.Config) {
	cfg, err := config.GetConfigWithContext(kContext)
	if err != nil {
		fmt.Printf("unable to get kubeconfig for context %q: %s", kContext, err)
		os.Exit(1)
	}
	gomega.ExpectWithOffset(1, cfg).NotTo(gomega.BeNil())

	err = kueue.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = kueuealpha.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = visibility.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = jobset.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = kftraining.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = kfmpi.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	client, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme.Scheme})
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	return client, cfg
}

func CreateVisibilityClient(user string) visibilityv1beta1.VisibilityV1beta1Interface {
	cfg, err := config.GetConfigWithContext("")
	if err != nil {
		fmt.Printf("unable to get kubeconfig: %s", err)
		os.Exit(1)
	}
	gomega.ExpectWithOffset(1, cfg).NotTo(gomega.BeNil())

	if user != "" {
		cfg.Impersonate.UserName = user
	}

	kueueClient, err := kueueclientset.NewForConfig(cfg)
	if err != nil {
		fmt.Printf("unable to create kueue clientset: %s", err)
		os.Exit(1)
	}
	visibilityClient := kueueClient.VisibilityV1beta1()
	return visibilityClient
}

func waitForOperatorAvailability(ctx context.Context, k8sClient client.Client, key types.NamespacedName) {
	deployment := &appsv1.Deployment{}
	pods := &corev1.PodList{}
	gomega.EventuallyWithOffset(2, func(g gomega.Gomega) error {
		g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
		g.Expect(k8sClient.List(ctx, pods, client.InNamespace(key.Namespace), client.MatchingLabels(deployment.Spec.Selector.MatchLabels))).To(gomega.Succeed())
		for _, pod := range pods.Items {
			for _, cs := range pod.Status.ContainerStatuses {
				// To make sure that we don't have restarts of controller-manager.
				// If we have that's mean that something went wrong, and there is
				// no needs to continue trying check availability.
				if cs.RestartCount > 0 {
					return gomega.StopTrying(fmt.Sprintf("%q in %q has restarted %d times", cs.Name, pod.Name, cs.RestartCount))
				}
			}
		}
		// To verify that webhooks are ready, checking is deployment have condition Available=True.
		g.Expect(deployment.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
			appsv1.DeploymentCondition{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
			cmpopts.IgnoreFields(appsv1.DeploymentCondition{}, "Reason", "Message", "LastUpdateTime", "LastTransitionTime")),
		))
		return nil
	}, StartUpTimeout, Interval).Should(gomega.Succeed())
}

func WaitForKueueAvailability(ctx context.Context, k8sClient client.Client) {
	kcmKey := types.NamespacedName{Namespace: "kueue-system", Name: "kueue-controller-manager"}
	waitForOperatorAvailability(ctx, k8sClient, kcmKey)
}

func WaitForJobSetAvailability(ctx context.Context, k8sClient client.Client) {
	jcmKey := types.NamespacedName{Namespace: "jobset-system", Name: "jobset-controller-manager"}
	waitForOperatorAvailability(ctx, k8sClient, jcmKey)
}

func WaitForKubeFlowTrainingOperatorAvailability(ctx context.Context, k8sClient client.Client) {
	kftoKey := types.NamespacedName{Namespace: "kubeflow", Name: "training-operator"}
	waitForOperatorAvailability(ctx, k8sClient, kftoKey)
}

func WaitForKubeFlowMPIOperatorAvailability(ctx context.Context, k8sClient client.Client) {
	kftoKey := types.NamespacedName{Namespace: "mpi-operator", Name: "mpi-operator"}
	waitForOperatorAvailability(ctx, k8sClient, kftoKey)
}
