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

package kueuectl

import (
	"bytes"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/rest"
	"k8s.io/utils/clock"

	"sigs.k8s.io/kueue/cmd/kueuectl/app"
)

type kueuectlRunOptions struct {
	clock            clock.Clock
	defaultNamespace *string
	errorMatcher     types.GomegaMatcher
}

type kueuectlRunOption func(*kueuectlRunOptions)

func withClock(c clock.Clock) kueuectlRunOption {
	return func(opts *kueuectlRunOptions) {
		opts.clock = c
	}
}

func withDefaultNamespace(ns string) kueuectlRunOption {
	return func(opts *kueuectlRunOptions) {
		opts.defaultNamespace = new(ns)
	}
}

func withErrorMatcher(matcher types.GomegaMatcher) kueuectlRunOption {
	return func(opts *kueuectlRunOptions) {
		opts.errorMatcher = matcher
	}
}

// executeKueuectl runs kueuectl in-process against cfg and returns its stdout and stderr.
func executeKueuectl(cfg *rest.Config, args []string, options ...kueuectlRunOption) (*bytes.Buffer, *bytes.Buffer) {
	ginkgo.GinkgoHelper()
	opts := kueuectlRunOptions{
		clock:        clock.RealClock{},
		errorMatcher: gomega.Not(gomega.HaveOccurred()),
	}
	for _, opt := range options {
		opt(&opts)
	}

	streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
	configFlags := genericclioptions.
		NewConfigFlags(true).
		WithDiscoveryQPS(50.0).
		WithWarningPrinter(streams).
		WithWrapConfigFn(func(*rest.Config) *rest.Config {
			return cfg
		})
	if opts.defaultNamespace != nil {
		configFlags.Namespace = opts.defaultNamespace
	}
	kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams, Clock: opts.clock})
	kueuectl.SetOut(output)
	kueuectl.SetErr(errOutput)
	kueuectl.SetArgs(args)
	err := kueuectl.Execute()
	gomega.Expect(err).To(opts.errorMatcher, "%s: %s %s", err, output, errOutput)
	return output, errOutput
}
