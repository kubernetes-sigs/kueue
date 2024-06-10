package testing

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	k8s "k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/clientcmd"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/client-go/clientset/versioned"
	kjobctlfake "sigs.k8s.io/kueue/cmd/experimental/kjobctl/client-go/clientset/versioned/fake"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/util"
)

type TestClientGetter struct {
	util.ClientGetter

	k8sClientset     k8s.Interface
	kjobctlClientset versioned.Interface

	configFlags *genericclioptions.TestConfigFlags
}

func NewTestClientGetter() *TestClientGetter {
	clientConfig := &clientcmd.DeferredLoadingClientConfig{}
	configFlags := genericclioptions.NewTestConfigFlags().
		WithClientConfig(clientConfig).
		WithNamespace(metav1.NamespaceDefault)
	return &TestClientGetter{
		ClientGetter:     util.NewClientGetter(configFlags),
		kjobctlClientset: kjobctlfake.NewSimpleClientset(),
		k8sClientset:     k8sfake.NewSimpleClientset(),
		configFlags:      configFlags,
	}
}

func (cg *TestClientGetter) WithNamespace(ns string) *TestClientGetter {
	cg.configFlags.WithNamespace(ns)
	return cg
}

func (cg *TestClientGetter) WithK8sClientset(clientset k8s.Interface) *TestClientGetter {
	cg.k8sClientset = clientset
	return cg
}

func (cg *TestClientGetter) WithKjobctlClientset(clientset versioned.Interface) *TestClientGetter {
	cg.kjobctlClientset = clientset
	return cg
}

func (cg *TestClientGetter) K8sClientset() (k8s.Interface, error) {
	return cg.k8sClientset, nil
}

func (cg *TestClientGetter) KjobctlClientset() (versioned.Interface, error) {
	return cg.kjobctlClientset, nil
}
