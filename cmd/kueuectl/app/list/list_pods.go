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

package list

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/cli-runtime/pkg/resource"
	k8s "k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/client-go/clientset/versioned/scheme"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/util"
	kueuejob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	kueuejobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	kueuemxjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/mxjob"
	kueuepaddlejob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/paddlejob"
	kueuepytorchjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/pytorchjob"
	kueuetfjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/tfjob"
	kueuexgboostjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/xgboostjob"
	kueuempijob "sigs.k8s.io/kueue/pkg/controller/jobs/mpijob"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	kueueraycluster "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	kueuerayjob "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
)

const (
	podLong = `Lists all pods that matches the given criteria: Should be part of the specified Job kind,
	belonging to the specified namespace, matching
	the label selector or the field selector.`
	podExample = `  # List Pods
  kueuectl list pods --for job/job-name`
)

var jobControllersWithPodLabelSelector = []jobControllerWithPodLabelSelector{
	&kueuejob.Job{},
	&kueuejobset.JobSet{},
	&kueuemxjob.JobControl{},
	&kueuepaddlejob.JobControl{},
	&kueuetfjob.JobControl{},
	&kueuepytorchjob.JobControl{},
	&kueuexgboostjob.JobControl{},
	&kueuempijob.MPIJob{},
	&pod.Pod{},
	&kueueraycluster.RayCluster{},
	&kueuerayjob.RayJob{},
}

type PodOptions struct {
	PrintFlags *genericclioptions.PrintFlags

	Limit                  int64
	AllNamespaces          bool
	Namespace              string
	LabelSelector          string
	FieldSelector          string
	UserSpecifiedForObject string
	ForName                string
	ForGVK                 schema.GroupVersionKind
	ForObject              *unstructured.Unstructured
	PodLabelSelector       string

	Clientset k8s.Interface

	genericiooptions.IOStreams
}

type jobControllerWithPodLabelSelector interface {
	Object() client.Object
	GVK() schema.GroupVersionKind
	PodLabelSelector() string
}

func NewPodOptions(streams genericiooptions.IOStreams) *PodOptions {
	return &PodOptions{
		PrintFlags: genericclioptions.NewPrintFlags("").WithTypeSetter(scheme.Scheme),
		IOStreams:  streams,
	}
}

func NewPodCmd(clientGetter util.ClientGetter, streams genericiooptions.IOStreams) *cobra.Command {
	o := NewPodOptions(streams)

	cmd := &cobra.Command{
		Use:                   "pods --for [<type>[.<api-group>]/]<name>",
		DisableFlagsInUseLine: true,
		Aliases:               []string{"po"},
		Short:                 "List Pods belong to a Job Kind",
		Long:                  podLong,
		Example:               podExample,
		Run: func(cmd *cobra.Command, args []string) {
			cobra.CheckErr(o.Complete(clientGetter))
			cobra.CheckErr(o.Run(cmd.Context()))
		},
	}

	o.PrintFlags.AddFlags(cmd)

	addAllNamespacesFlagVar(cmd, &o.AllNamespaces)
	addFieldSelectorFlagVar(cmd, &o.FieldSelector)
	addLabelSelectorFlagVar(cmd, &o.LabelSelector)
	addForObjectFlagVar(cmd, &o.UserSpecifiedForObject)

	_ = cmd.MarkFlagRequired("for")

	return cmd
}

// Complete takes the command arguments and infers any remaining options.
func (o *PodOptions) Complete(clientGetter util.ClientGetter) error {
	var err error

	o.Limit, err = listRequestLimit()
	if err != nil {
		return err
	}
	o.Namespace, _, err = clientGetter.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return err
	}

	o.Clientset, err = clientGetter.K8sClientSet()
	if err != nil {
		return err
	}

	mapper, err := clientGetter.ToRESTMapper()
	if err != nil {
		return err
	}
	var found bool
	o.ForGVK, o.ForName, found, err = decodeResourceTypeName(mapper, o.UserSpecifiedForObject)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("invalid value '%s' used in --for flag; value must be in the format [TYPE[.API-GROUP]/]NAME", o.UserSpecifiedForObject)
	}

	infos, err := o.fetchDynamicResourceInfos(clientGetter)
	if err != nil {
		return err
	}

	if len(infos) == 0 {
		if !o.AllNamespaces {
			fmt.Fprintf(o.ErrOut, "No resources found in %s namespace.\n", o.Namespace)
		} else {
			fmt.Fprintln(o.ErrOut, "No resources found")
		}
		return nil
	}

	o.ForObject, err = o.getForObject(infos)
	if err != nil {
		return err
	}

	o.PodLabelSelector, err = o.getPodLabelSelector()
	if err != nil {
		return err
	}

	return nil
}

// fetchDynamicResourceInfos builds and executes a dynamic client query for a resource specified in --for
func (o *PodOptions) fetchDynamicResourceInfos(clientGetter util.ClientGetter) ([]*resource.Info, error) {
	r := clientGetter.NewResourceBuilder().
		Unstructured().
		NamespaceParam(o.Namespace).
		DefaultNamespace().
		AllNamespaces(o.AllNamespaces).
		FieldSelectorParam(fmt.Sprintf("metadata.name=%s", o.ForName)).
		ResourceTypeOrNameArgs(true, o.ForGVK.Kind).
		ContinueOnError().
		Latest().
		Flatten().
		Do()

	if r == nil {
		return nil, fmt.Errorf("Error building client for: %s/%s", o.ForGVK.Kind, o.ForName)
	}

	if err := r.Err(); err != nil {
		return nil, err
	}

	infos, err := r.Infos()
	if err != nil {
		return nil, err
	}

	return infos, nil
}

func (o *PodOptions) getForObject(infos []*resource.Info) (*unstructured.Unstructured, error) {
	job, ok := infos[0].Object.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("Invalid object %+v. Unexpected type %T", job, infos[0].Object)
	}

	return job, nil
}

func (o *PodOptions) ToPrinter(headers bool) (printers.ResourcePrinterFunc, error) {
	if !o.PrintFlags.OutputFlagSpecified() {
		printer := newPodTablePrinter().
			WithNamespace(o.AllNamespaces).
			WithNoHeaders(headers)
		return printer.PrintObj, nil
	}

	printer, err := o.PrintFlags.ToPrinter()
	if err != nil {
		return nil, err
	}

	return printer.PrintObj, nil
}

// Run prints the pods for a specific Job
func (o *PodOptions) Run(ctx context.Context) error {
	var totalCount int

	namespace := o.Namespace
	if o.AllNamespaces {
		namespace = ""
	}

	if len(o.LabelSelector) != 0 {
		o.PodLabelSelector = "," + o.PodLabelSelector
	}

	opts := metav1.ListOptions{
		LabelSelector: o.LabelSelector + o.PodLabelSelector,
		FieldSelector: o.FieldSelector,
		Limit:         o.Limit,
	}

	tabWriter := printers.GetNewTabWriter(o.Out)
	for {
		podList, err := o.Clientset.CoreV1().Pods(namespace).List(ctx, opts)
		if err != nil {
			return err
		}

		totalCount += len(podList.Items)
		headers := totalCount == 0

		printer, err := o.ToPrinter(headers)
		if err != nil {
			return err
		}

		if err = printer.PrintObj(podList, tabWriter); err != nil {
			return err
		}

		if podList.Continue != "" {
			opts.Continue = podList.Continue
			continue
		}

		// handle if no filtered podList found
		if totalCount == 0 {
			if !o.AllNamespaces {
				fmt.Fprintf(o.ErrOut, "No resources found in %s namespace.\n", o.Namespace)
			} else {
				fmt.Fprintln(o.ErrOut, "No resources found.")
			}
			return nil
		}

		if err = tabWriter.Flush(); err != nil {
			return err
		}

		return nil
	}
}

func (o *PodOptions) getJobController() jobControllerWithPodLabelSelector {
	for _, jobController := range jobControllersWithPodLabelSelector {
		if jobController.GVK() == o.ForGVK {
			return jobController
		}
	}
	return nil
}

// getPodLabelSelector returns the podLabels used as a standard selector for jobs
func (o *PodOptions) getPodLabelSelector() (string, error) {
	jobController := o.getJobController()
	if jobController == nil {
		return "", fmt.Errorf("unsupported kind: %s", o.ForObject.GetKind())
	}

	err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.ForObject.UnstructuredContent(), jobController.Object())
	if err != nil {
		return "", fmt.Errorf("failed to convert unstructured object: %w", err)
	}

	return jobController.PodLabelSelector(), nil
}
