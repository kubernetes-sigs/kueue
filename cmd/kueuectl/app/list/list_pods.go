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
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/printers"
	k8s "k8s.io/client-go/kubernetes"

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

	Clientset k8s.Interface

	genericiooptions.IOStreams
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

	// parse --for flag
	if o.UserSpecifiedForObject != "" {
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
			return fmt.Errorf("Error building client for: %s/%s", o.ForGVK.Kind, o.ForName)
		}
		if err := r.Err(); err != nil {
			return err
		}
		infos, err := r.Infos()
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

		job, ok := infos[0].Object.(*unstructured.Unstructured)
		if !ok {
			fmt.Fprintf(o.ErrOut, "Invalid object %+v. Unexpected type %T", job, infos[0].Object)
			return nil
		}
		o.ForObject = job
	}

	return nil
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
	err := o.listPods(ctx)
	if err != nil {
		return err
	}

	return nil
}

// listPods lists the pods based on the given --for object
func (o *PodOptions) listPods(ctx context.Context) error {
	var totalCount int

	namespace := o.Namespace
	if o.AllNamespaces {
		namespace = ""
	}

	podLabelSelector, err := o.getPodLabelSelector()
	if err != nil {
		return err
	}

	if len(o.LabelSelector) != 0 {
		podLabelSelector = "," + podLabelSelector
	}

	opts := metav1.ListOptions{
		LabelSelector: o.LabelSelector + podLabelSelector,
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

// getPodLabelSelector returns the podLabels used as a standard selector for jobs
func (o *PodOptions) getPodLabelSelector() (string, error) {
	// Helper function to handle the conversion
	getSelector := func(dest interface{}) (string, error) {
		err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.ForObject.UnstructuredContent(), dest)
		if err != nil {
			return "", fmt.Errorf("failed to convert unstructured object: %w", err)
		}

		switch v := dest.(type) {
		case interface{ PodLabelSelector() string }:
			return v.PodLabelSelector(), nil
		default:
			return "", errors.New("unsupported type for getting PodLabelSelector")
		}
	}

	// getSelector based on ForObject kind. Only limited to job integrations.
	switch strings.ToLower(o.ForObject.GetKind()) {
	case "job":
		return getSelector(&kueuejob.Job{})
	case "jobset":
		return getSelector(&kueuejobset.JobSet{})
	case "mpijob":
		return getSelector(&kueuempijob.MPIJob{})
	case "mxjob":
		return getSelector(&kueuemxjob.JobControl{})
	case "paddlejob":
		return getSelector(&kueuepaddlejob.JobControl{})
	case "tfjob":
		return getSelector(&kueuetfjob.JobControl{})
	case "pytorchjob":
		return getSelector(&kueuepytorchjob.JobControl{})
	case "xgboostjob":
		return getSelector(&kueuexgboostjob.JobControl{})
	case "raycluster":
		return getSelector(&kueueraycluster.RayCluster{})
	case "rayjob":
		return getSelector(&kueuerayjob.RayJob{})
	default:
		return "", fmt.Errorf("unsupported kind: %s", o.ForObject.GetKind())
	}
}
