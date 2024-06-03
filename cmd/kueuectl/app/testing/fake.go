package testing

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/clientcmd"

	"sigs.k8s.io/kueue/client-go/clientset/versioned"
	"sigs.k8s.io/kueue/client-go/clientset/versioned/fake"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/util"
)

type TestClientGetter struct {
	util.ClientGetter

	ClientSet versioned.Interface

	configFlags *genericclioptions.TestConfigFlags
}

var _ util.ClientGetter = (*TestClientGetter)(nil)

func NewTestClientGetter() *TestClientGetter {
	clientConfig := &clientcmd.DeferredLoadingClientConfig{}
	configFlags := genericclioptions.NewTestConfigFlags().
		WithClientConfig(clientConfig).
		WithNamespace(metav1.NamespaceDefault)
	return &TestClientGetter{
		ClientGetter: util.NewClientGetter(configFlags),
		ClientSet:    fake.NewSimpleClientset(),
		configFlags:  configFlags,
	}
}

func (f *TestClientGetter) WithNamespace(ns string) *TestClientGetter {
	f.configFlags.WithNamespace(ns)
	return f
}

func (f *TestClientGetter) KueueClientSet() (versioned.Interface, error) {
	return f.ClientSet, nil
}
