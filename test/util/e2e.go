package util

import (
	"fmt"
	"os"

	"github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	visibility "sigs.k8s.io/kueue/apis/visibility/v1alpha1"
	kueueclientset "sigs.k8s.io/kueue/client-go/clientset/versioned"
	visibilityv1alpha1 "sigs.k8s.io/kueue/client-go/clientset/versioned/typed/visibility/v1alpha1"
)

func CreateClientUsingCluster(kContext string) client.Client {
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

	client, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	return client
}

func CreateVisibilityClient(user string) visibilityv1alpha1.VisibilityV1alpha1Interface {
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
	visibilityClient := kueueClient.VisibilityV1alpha1()
	return visibilityClient
}
