package util

import (
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"sigs.k8s.io/kueue/client-go/clientset/versioned"
)

type ClientGetter interface {
	genericclioptions.RESTClientGetter

	KueueClientSet() (versioned.Interface, error)
}

type clientGetterImpl struct {
	genericclioptions.RESTClientGetter
}

var _ ClientGetter = (*clientGetterImpl)(nil)

func NewClientGetter(clientGetter genericclioptions.RESTClientGetter) ClientGetter {
	return &clientGetterImpl{
		RESTClientGetter: clientGetter,
	}
}

func (f *clientGetterImpl) KueueClientSet() (versioned.Interface, error) {
	config, err := f.ToRESTConfig()
	if err != nil {
		return nil, err
	}

	clientset, err := versioned.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return clientset, nil
}
