package kueuectl

import (
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/rest"
)

func CreateConfigFlagsWithRestConfig(cfg *rest.Config, streams genericiooptions.IOStreams) *genericclioptions.ConfigFlags {
	return genericclioptions.
		NewConfigFlags(true).
		WithDiscoveryQPS(50.0).
		WithWarningPrinter(streams).
		WithWrapConfigFn(func(*rest.Config) *rest.Config {
			return cfg
		})
}
