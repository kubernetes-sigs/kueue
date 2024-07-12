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
	"fmt"
	"strings"
	"time"

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
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/kubectl/pkg/cmd/attach"
	"k8s.io/kubectl/pkg/cmd/exec"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/builder"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/completion"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/util"
)

const (
	createJobExample = `  # Create job 
  kjobctl create job \ 
	--profile my-application-profile  \
	--cmd "sleep 5" \
	--parallelism 4 \
	--completions 4 \ 
	--request cpu=500m,ram=4Gi \
	--localqueue my-local-queue-name`
	createInteractiveExample = `  # Create interactive 
  kjobctl create interactive \ 
	--profile my-application-profile  \
	--pod-running-timeout 30s \
	--rm`
	profileFlagName     = "profile"
	commandFlagName     = "cmd"
	parallelismFlagName = "parallelism"
	completionsFlagName = "completions"
	requestFlagName     = "request"
	localQueueFlagName  = "localqueue"
	podRunningTimeout   = "pod-running-timeout"
)

var (
	podRunningTimeoutDefault = 1 * time.Minute
)

type CreateOptions struct {
	exec.StreamOptions

	PrintFlags *genericclioptions.PrintFlags
	Config     *restclient.Config
	Attach     attach.RemoteAttach
	AttachFunc func(*CreateOptions, *corev1.Container, remotecommand.TerminalSizeQueue, *corev1.Pod) func() error

	DryRunStrategy util.DryRunStrategy

	Namespace   string
	ProfileName string
	ModeName    v1alpha1.ApplicationProfileMode

	Command              []string
	Parallelism          *int32
	Completions          *int32
	Requests             corev1.ResourceList
	LocalQueue           string
	PodRunningTimeout    time.Duration
	RemoveInteractivePod bool

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
		StreamOptions: exec.StreamOptions{
			IOStreams: streams,
		},
		Attach:     &attach.DefaultRemoteAttach{},
		AttachFunc: defaultAttachFunc,
	}
}

type modeSubcommand struct {
	ModeName v1alpha1.ApplicationProfileMode
	Setup    func(subcmd *cobra.Command, o *CreateOptions)
}

var createModeSubcommands = map[string]modeSubcommand{
	"job": {
		ModeName: v1alpha1.JobMode,
		Setup: func(subcmd *cobra.Command, o *CreateOptions) {
			subcmd.Use += " [--parallelism PARALLELISM] [--completions COMPLETIONS]"
			subcmd.Short = "Create a job"
			subcmd.Example = createJobExample
			subcmd.Flags().Int32Var(&o.UserSpecifiedParallelism, parallelismFlagName, 0,
				"Parallelism specifies the maximum desired number of pods the job should run at any given time.")
			subcmd.Flags().Int32Var(&o.UserSpecifiedCompletions, completionsFlagName, 0,
				"Completions specifies the desired number of successfully finished pods.")
		},
	},
	"interactive": {
		ModeName: v1alpha1.InteractiveMode,
		Setup: func(subcmd *cobra.Command, o *CreateOptions) {
			subcmd.Use += " [--pod-running-timeout DURATION] [--rm]"
			subcmd.Short = "Create an interactive shell"
			subcmd.Example = createInteractiveExample
			subcmd.Flags().DurationVar(&o.PodRunningTimeout, podRunningTimeout, podRunningTimeoutDefault,
				"The length of time (like 5s, 2m, or 3h, higher than zero) to wait until at least one pod is running.")
			subcmd.Flags().BoolVar(&o.RemoveInteractivePod, "rm", false,
				"Remove pod when interactive session exits.")
		},
	},
}

func NewCreateCmd(clientGetter util.ClientGetter, streams genericiooptions.IOStreams, clock clock.Clock) *cobra.Command {
	o := NewCreateOptions(streams)

	cmd := &cobra.Command{
		Use:     "create",
		Short:   "Create a task",
		Example: fmt.Sprintf("%s\n\n%s", createJobExample, createInteractiveExample),
	}

	for modeName, modeSubcommand := range createModeSubcommands {
		subcmd := &cobra.Command{
			Use: fmt.Sprintf("%s"+
				" --profile APPLICATION_PROFILE_NAME"+
				" [--cmd COMMAND]"+
				" [--request RESOURCE_NAME=QUANTITY]"+
				" [--localqueue LOCAL_QUEUE_NAME]", modeName),
			DisableFlagsInUseLine: true,
			Args:                  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				cmd.SilenceUsage = true

				err := o.Complete(clientGetter, cmd)
				if err != nil {
					return err
				}

				return o.Run(cmd.Context(), clientGetter, clock.Now())
			},
		}

		o.PrintFlags.AddFlags(subcmd)

		subcmd.Flags().StringVarP(&o.ProfileName, profileFlagName, "p", "",
			"Application profile contains a template (with defaults set) for running a specific type of application.")
		subcmd.Flags().StringVar(&o.UserSpecifiedCommand, commandFlagName, "",
			"Command which is associated with the resource.")
		subcmd.Flags().StringToStringVar(&o.UserSpecifiedRequest, requestFlagName, nil,
			"Request is a set of (resource name, quantity) pairs.")
		subcmd.Flags().StringVar(&o.LocalQueue, localQueueFlagName, "",
			"Kueue localqueue name which is associated with the resource.")
		modeSubcommand.Setup(subcmd, o)

		util.AddDryRunFlag(subcmd)

		_ = cmd.MarkFlagRequired(profileFlagName)

		cobra.CheckErr(subcmd.RegisterFlagCompletionFunc(profileFlagName, completion.ApplicationProfileNameFunc(clientGetter)))
		cobra.CheckErr(subcmd.RegisterFlagCompletionFunc(localQueueFlagName, completion.LocalQueueNameFunc(clientGetter)))

		cmd.AddCommand(subcmd)
	}

	return cmd
}

func (o *CreateOptions) Complete(clientGetter util.ClientGetter, cmd *cobra.Command) error {
	currentSubcommand := createModeSubcommands[cmd.Name()]
	o.ModeName = currentSubcommand.ModeName

	var err error

	o.Namespace, _, err = clientGetter.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return err
	}

	if o.UserSpecifiedCommand != "" {
		o.Command = strings.Fields(o.UserSpecifiedCommand)
	}

	if cmd.Flags().Changed(parallelismFlagName) {
		o.Parallelism = ptr.To(o.UserSpecifiedParallelism)
	}

	if cmd.Flags().Changed(completionsFlagName) {
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

	if cmd.Flags().Changed(podRunningTimeout) && o.PodRunningTimeout <= 0 {
		return errors.New("--pod-running-timeout must be higher than zero")
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

	o.Config, err = clientGetter.ToRESTConfig()
	if err != nil {
		return err
	}

	err = setKubernetesDefaults(o.Config)
	if err != nil {
		return err
	}

	return nil
}

func (o *CreateOptions) Run(ctx context.Context, clientGetter util.ClientGetter, runTime time.Time) error {
	obj, err := builder.NewBuilder(clientGetter, runTime).
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

	err = o.PrintObj(obj, o.Out)
	if err != nil {
		return err
	}

	if o.ModeName == v1alpha1.InteractiveMode {
		pod := obj.(*corev1.Pod)
		return o.RunInteractivePod(ctx, clientGetter, pod.Name)
	}

	return nil
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

func (o *CreateOptions) RunInteractivePod(ctx context.Context, clientGetter util.ClientGetter, podName string) error {
	k8sclient, err := clientGetter.K8sClientset()
	if err != nil {
		return err
	}

	if o.RemoveInteractivePod {
		defer func() {
			err = k8sclient.CoreV1().Pods(o.Namespace).Delete(ctx, podName, metav1.DeleteOptions{})
			if err != nil {
				fmt.Fprintln(o.ErrOut, err.Error())
			}
			fmt.Fprintf(o.Out, "pod \"%s\" deleted\n", podName)
		}()
	}

	fmt.Fprintf(o.Out, "waiting for pod \"%s\" to be running...\n", podName)
	err = waitForPodRunning(ctx, k8sclient, o.Namespace, podName, o.PodRunningTimeout)
	if err != nil {
		return err
	}

	pod, err := k8sclient.CoreV1().Pods(o.Namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	err = attachTTY(o, pod)
	if err != nil {
		return err
	}

	return nil
}

func attachTTY(o *CreateOptions, pod *corev1.Pod) error {
	o.Stdin = true
	o.TTY = true
	containerToAttach := &pod.Spec.Containers[0]
	if !containerToAttach.TTY {
		return fmt.Errorf("error: Unable to use a TTY - container %s did not allocate one", containerToAttach.Name)
	}

	tty := o.SetupTTY()

	var sizeQueue remotecommand.TerminalSizeQueue
	if tty.Raw {
		// this call spawns a goroutine to monitor/update the terminal size
		sizeQueue = tty.MonitorSize(tty.GetSize())

		// unset p.Err if it was previously set because both stdout and stderr go over p.Out when tty is true
		o.ErrOut = nil
	}

	return tty.Safe(o.AttachFunc(o, containerToAttach, sizeQueue, pod))
}

func defaultAttachFunc(o *CreateOptions, containerToAttach *corev1.Container, sizeQueue remotecommand.TerminalSizeQueue, pod *corev1.Pod) func() error {
	return func() error {
		restClient, err := restclient.RESTClientFor(o.Config)
		if err != nil {
			return err
		}

		req := restClient.Post().
			Resource("pods").
			Name(pod.Name).
			Namespace(pod.Namespace).
			SubResource("attach")
		req.VersionedParams(&corev1.PodAttachOptions{
			Container: containerToAttach.Name,
			Stdin:     o.Stdin,
			Stdout:    o.Out != nil,
			Stderr:    o.ErrOut != nil,
			TTY:       o.TTY,
		}, scheme.ParameterCodec)

		return o.Attach.Attach(req.URL(), o.Config, o.In, o.Out, o.ErrOut, o.TTY, sizeQueue)
	}
}
