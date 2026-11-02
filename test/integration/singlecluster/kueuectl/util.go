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

type commandOutputMode int

const (
	commandOutputSeparate commandOutputMode = iota + 1
	commandOutputCombined
)

type kueuectlRunOptions struct {
	clock                clock.Clock
	defaultNamespace     *string
	commandOutput        commandOutputMode
	errorMatcher         types.GomegaMatcher
	omitErrorDescription bool
}

func executeKueuectl(cfg *rest.Config, args []string, opts kueuectlRunOptions) (*bytes.Buffer, *bytes.Buffer) {
	ginkgo.GinkgoHelper()
	streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
	configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
	if opts.defaultNamespace != nil {
		configFlags.Namespace = opts.defaultNamespace
	}
	kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams, Clock: opts.clock})
	// Cobra's output writers are independent of the application's IOStreams.
	// Preserve their routing without changing streams.ErrOut or the warning printer.
	switch opts.commandOutput {
	case commandOutputSeparate:
		kueuectl.SetOut(output)
		kueuectl.SetErr(errOutput)
	case commandOutputCombined:
		kueuectl.SetOut(output)
		kueuectl.SetErr(output)
	}
	kueuectl.SetArgs(args)
	err := kueuectl.Execute()
	matcher := opts.errorMatcher
	if matcher == nil {
		matcher = gomega.Not(gomega.HaveOccurred())
	}
	switch {
	case opts.omitErrorDescription:
		gomega.Expect(err).To(matcher)
	case opts.commandOutput == commandOutputSeparate:
		gomega.Expect(err).To(matcher, "%s: %s %s", err, output, errOutput)
	default:
		gomega.Expect(err).To(matcher, "%s: %s", err, output)
	}
	return output, errOutput
}

func CreateConfigFlagsWithRestConfig(cfg *rest.Config, streams genericiooptions.IOStreams) *genericclioptions.ConfigFlags {
	return genericclioptions.
		NewConfigFlags(true).
		WithDiscoveryQPS(50.0).
		WithWarningPrinter(streams).
		WithWrapConfigFn(func(*rest.Config) *rest.Config {
			return cfg
		})
}
