/*
Copyright 2024 The Kubernetes Authors.

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

package create

import (
	"context"
	"errors"
	"strings"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/builder"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/completion"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/util"
)

const (
	createExample = `  # Create job 
  kjobctl create job \ 
	--profile my-application-profile  \
	--cmd "sleep 5" \
	--parallelism 4 \
	--completions 4 \ 
	--request cpu=500m,ram=4Gi \
	--localqueue my-local-queue-name`
	profileFlagName     = "profile"
	commandFlagName     = "cmd"
	parallelismFlagName = "parallelism"
	completionsFlagName = "completions"
	requestFlagName     = "request"
	localQueueFlagName  = "localqueue"
)

var (
	invalidApplicationProfileModeErr = errors.New("invalid application profile mode")
)

type CreateOptions struct {
	PrintFlags *genericclioptions.PrintFlags

	DryRunStrategy util.DryRunStrategy

	Namespace   string
	ProfileName string
	ModeName    v1alpha1.ApplicationProfileMode

	Command     []string
	Parallelism *int32
	Completions *int32
	Requests    corev1.ResourceList
	LocalQueue  string

	UserSpecifiedCommand     string
	UserSpecifiedParallelism int32
	UserSpecifiedCompletions int32
	UserSpecifiedRequest     map[string]string

	PrintObj printers.ResourcePrinterFunc

	genericiooptions.IOStreams
}

func NewCreateOptions(streams genericiooptions.IOStreams) *CreateOptions {
	return &CreateOptions{
		PrintFlags: genericclioptions.NewPrintFlags("created").WithTypeSetter(scheme.Scheme),
		IOStreams:  streams,
	}
}

func NewCreateCmd(clientGetter util.ClientGetter, streams genericiooptions.IOStreams) *cobra.Command {
	o := NewCreateOptions(streams)

	cmd := &cobra.Command{
		Use: "create job|interactive" +
			" --profile APPLICATION_PROFILE_NAME" +
			" [--cmd COMMAND]" +
			" [--parallelism PARALLELISM]" +
			" [--completions COMPLETIONS]" +
			" [--request RESOURCE_NAME=QUANTITY]" +
			" [--localqueue LOCAL_QUEUE_NAME]" +
			" [--dry-run STRATEGY]",
		DisableFlagsInUseLine: true,
		Short:                 "Create a resource",
		Example:               createExample,
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		ValidArgs:             []string{"job", "interactive"},
		Run: func(cmd *cobra.Command, args []string) {
			cobra.CheckErr(o.Complete(clientGetter, cmd, args))
			cobra.CheckErr(o.Run(cmd.Context(), clientGetter))
		},
	}

	o.PrintFlags.AddFlags(cmd)

	cmd.Flags().StringVarP(&o.ProfileName, profileFlagName, "p", "",
		"Application profile contains a template (with defaults set) for running a specific type of application.")
	cmd.Flags().StringVar(&o.UserSpecifiedCommand, commandFlagName, "",
		"Command which is associated with the resource.")
	cmd.Flags().Int32Var(&o.UserSpecifiedParallelism, parallelismFlagName, 0,
		"Parallelism specifies the maximum desired number of pods the job should run at any given time.")
	cmd.Flags().Int32Var(&o.UserSpecifiedCompletions, completionsFlagName, 0,
		"Completions specifies the desired number of successfully finished pods the.")
	cmd.Flags().StringToStringVar(&o.UserSpecifiedRequest, requestFlagName, nil,
		"Request is a set of (resource name, quantity) pairs.")
	cmd.Flags().StringVar(&o.LocalQueue, localQueueFlagName, "",
		"Kueue localqueue name which is associated with the resource.")

	util.AddDryRunFlag(cmd)

	_ = cmd.MarkFlagRequired(profileFlagName)

	cobra.CheckErr(cmd.RegisterFlagCompletionFunc(profileFlagName, completion.ApplicationProfileNameFunc(clientGetter)))
	cobra.CheckErr(cmd.RegisterFlagCompletionFunc(localQueueFlagName, completion.LocalQueueNameFunc(clientGetter)))

	return cmd
}

func (o *CreateOptions) Complete(clientGetter util.ClientGetter, cmd *cobra.Command, args []string) error {
	switch args[0] {
	case "job":
		o.ModeName = v1alpha1.JobMode
	case "interactive":
		o.ModeName = v1alpha1.InteractiveMode
	default:
		return invalidApplicationProfileModeErr
	}

	var err error

	o.Namespace, _, err = clientGetter.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return err
	}

	if o.UserSpecifiedCommand != "" {
		o.Command = strings.Fields(o.UserSpecifiedCommand)
	}

	if flag := cmd.Flags().Lookup(parallelismFlagName); flag.Changed {
		o.Parallelism = ptr.To(o.UserSpecifiedParallelism)
	}

	if flag := cmd.Flags().Lookup(completionsFlagName); flag.Changed {
		o.Completions = ptr.To(o.UserSpecifiedCompletions)
	}

	if len(o.UserSpecifiedRequest) > 0 {
		o.Requests = make(corev1.ResourceList)
		for key, value := range o.UserSpecifiedRequest {
			quantity, err := apiresource.ParseQuantity(value)
			if err != nil {
				return err
			}
			o.Requests[corev1.ResourceName(key)] = quantity
		}
	}

	o.DryRunStrategy, err = util.GetDryRunStrategy(cmd)
	if err != nil {
		return err
	}

	err = util.PrintFlagsWithDryRunStrategy(o.PrintFlags, o.DryRunStrategy)
	if err != nil {
		return err
	}

	printer, err := o.PrintFlags.ToPrinter()
	if err != nil {
		return err
	}

	o.PrintObj = printer.PrintObj

	return nil
}

func (o *CreateOptions) Run(ctx context.Context, clientGetter util.ClientGetter) error {
	obj, err := builder.NewBuilder(clientGetter).
		WithNamespace(o.Namespace).
		WithProfileName(o.ProfileName).
		WithModeName(o.ModeName).
		WithCommand(o.Command).
		WithParallelism(o.Parallelism).
		WithCompletions(o.Completions).
		WithRequests(o.Requests).
		WithLocalQueue(o.LocalQueue).
		Do(ctx)
	if err != nil {
		return err
	}

	if o.DryRunStrategy != util.DryRunClient {
		obj, err = o.createObject(ctx, clientGetter, obj)
		if err != nil {
			return err
		}
	}

	return o.PrintObj(obj, o.Out)
}

func (o *CreateOptions) createObject(ctx context.Context, clientGetter util.ClientGetter, obj runtime.Object) (runtime.Object, error) {
	options := metav1.CreateOptions{}
	if o.DryRunStrategy == util.DryRunServer {
		options.DryRun = []string{metav1.DryRunAll}
	}

	dynamicClient, err := clientGetter.DynamicClient()
	if err != nil {
		return nil, err
	}

	restMapper, err := clientGetter.ToRESTMapper()
	if err != nil {
		return nil, err
	}

	gvk := obj.GetObjectKind().GroupVersionKind()
	mapping, err := restMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, err
	}
	gvr := mapping.Resource

	unstructured := &unstructured.Unstructured{}
	unstructured.Object, err = runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}

	unstructured, err = dynamicClient.Resource(gvr).Namespace(o.Namespace).Create(ctx, unstructured, options)
	if err != nil {
		return nil, err
	}

	createdObj := obj.DeepCopyObject()

	err = runtime.DefaultUnstructuredConverter.FromUnstructured(unstructured.UnstructuredContent(), createdObj)
	if err != nil {
		return nil, err
	}

	return createdObj, nil
}
