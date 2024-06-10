package testing

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/resource"
	k8s "k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"

	"sigs.k8s.io/kueue/client-go/clientset/versioned"
	kueuefake "sigs.k8s.io/kueue/client-go/clientset/versioned/fake"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/util"
)

type TestClientGetter struct {
	util.ClientGetter

	KueueClientset     versioned.Interface
	K8sClientset       k8s.Interface
	UnstructuredClient resource.RESTClient

	configFlags *genericclioptions.TestConfigFlags
}

var _ util.ClientGetter = (*TestClientGetter)(nil)

func NewTestClientGetter() *TestClientGetter {
	clientConfig := &clientcmd.DeferredLoadingClientConfig{}
	configFlags := genericclioptions.NewTestConfigFlags().
		WithClientConfig(clientConfig).
		WithNamespace(metav1.NamespaceDefault)
	return &TestClientGetter{
		ClientGetter:   util.NewClientGetter(configFlags),
		KueueClientset: kueuefake.NewSimpleClientset(),
		K8sClientset:   k8sfake.NewSimpleClientset(),
		configFlags:    configFlags,
	}
}

func (f *TestClientGetter) WithNamespace(ns string) *TestClientGetter {
	f.configFlags.WithNamespace(ns)
	return f
}

func (f *TestClientGetter) WithRESTMapper(mapper meta.RESTMapper) *TestClientGetter {
	f.configFlags.WithRESTMapper(mapper)
	return f
}

func (f *TestClientGetter) KueueClientSet() (versioned.Interface, error) {
	return f.KueueClientset, nil
}

func (f *TestClientGetter) K8sClientSet() (k8s.Interface, error) {
	return f.K8sClientset, nil
}

func (f *TestClientGetter) NewResourceBuilder() *resource.Builder {
	return resource.NewFakeBuilder(
		func(version schema.GroupVersion) (resource.RESTClient, error) {
			return f.UnstructuredClient, nil
		},
		f.ToRESTMapper,
		func() (restmapper.CategoryExpander, error) {
			return resource.FakeCategoryExpander, nil
		},
	)
}
