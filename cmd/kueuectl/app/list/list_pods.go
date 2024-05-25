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
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/printers"
	k8s "k8s.io/client-go/kubernetes"
	"sigs.k8s.io/kueue/client-go/clientset/versioned/scheme"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/util"
)

const (
	podLong = `Lists all pods that matches the given criteria: Should be part of the specified Job,
	belonging to the specified namespace, matching
	the label selector or the field selector.`
	podExample = `  # List Pods
  kueuectl list pods --for job/job-name`
	jobControllerUIDLabel = "batch.kubernetes.io/controller-uid"
)

type PodOptions struct {
	PrintFlags *genericclioptions.PrintFlags

	Namespace     string
	LabelSelector string
	FieldSelector string
	JobArg        string

	Clientset k8s.Interface

	PrintObj printers.ResourcePrinterFunc

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
		Use:                   "pods [--job job/<job-name>]",
		DisableFlagsInUseLine: true,
		Aliases:               []string{"po"},
		Short:                 "List Pods belong to a Job",
		Long:                  podLong,
		Example:               podExample,
		Run: func(cmd *cobra.Command, args []string) {
			cobra.CheckErr(o.Complete(clientGetter, cmd, args))
			cobra.CheckErr(o.Validate())
			cobra.CheckErr(o.Run(cmd.Context()))
		},
	}

	o.PrintFlags.AddFlags(cmd)

	addFieldSelectorFlagVar(cmd, &o.FieldSelector)
	addLabelSelectorFlagVar(cmd, &o.LabelSelector)
	addJobFlagVar(cmd, &o.JobArg)

	return cmd
}

// Complete takes the command arguments and infers any remaining options.
func (o *PodOptions) Complete(clientGetter util.ClientGetter, cmd *cobra.Command, args []string) error {
	var err error

	o.Namespace, _, err = clientGetter.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return err
	}

	o.Clientset, err = clientGetter.K8sClientSet()
	if err != nil {
		return err
	}

	if !o.PrintFlags.OutputFlagSpecified() {
		o.PrintObj = printPodTable
	} else {
		printer, err := o.PrintFlags.ToPrinter()
		if err != nil {
			return err
		}
		o.PrintObj = printer.PrintObj
	}

	if len(args) > 0 {
		jobArg, err := cmd.Flags().GetString("job")
		if err != nil {
			return err
		}
		o.JobArg = jobArg
	}

	return nil
}

func (o *PodOptions) Validate() error {
	if !o.validJobFlagOptionProvided() {
		return errors.New("not a valid --job flag. Please provide a valid job name in format job/job-name")
	}
	return nil
}

func (o *PodOptions) validJobFlagOptionProvided() bool {
	jobArgSlice := strings.Split(o.JobArg, "/")

	// jobArgSlice should be ["job", "job-name"]
	if len(jobArgSlice) != 2 {
		return false
	}

	// valid only if the kind is a job
	kind := strings.ToLower(jobArgSlice[0])
	return strings.Contains(kind, "job")
}

// Run prints the pods for a specific Job
func (o *PodOptions) Run(ctx context.Context) error {
	jobName := strings.Split(o.JobArg, "/")[1]
	controllerUID, err := o.getJobControllerUID(ctx, jobName)
	if err != nil {
		return err
	}

	err = o.listPodsByControllerUID(ctx, controllerUID)
	if err != nil {
		return err
	}
	return nil
}

// getJobControllerUID fetches the controllerUID of the given Job
func (o *PodOptions) getJobControllerUID(ctx context.Context, jobName string) (string, error) {
	job, err := o.Clientset.BatchV1().Jobs(o.Namespace).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	return job.ObjectMeta.Labels[jobControllerUIDLabel], nil
}

// listPodsByControllerUID lists the pods based on the given controllerUID linked to the pod
func (o *PodOptions) listPodsByControllerUID(ctx context.Context, controllerUID string) error {
	continueToken := ""

	// assign or appends controllerUID label with the existing selector if any
	// used for filtering the pods which belongs to the job controller
	if o.LabelSelector != "" {
		o.LabelSelector = fmt.Sprintf("%s,%s=%s", o.LabelSelector, jobControllerUIDLabel, controllerUID)
	} else {
		o.LabelSelector = fmt.Sprintf("%s=%s", jobControllerUIDLabel, controllerUID)
	}

	for {
		podList, err := o.Clientset.CoreV1().Pods(o.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: o.LabelSelector,
			FieldSelector: o.FieldSelector,
			Continue:      continueToken,
		})
		if err != nil {
			return err
		}

		if len(podList.Items) == 0 {
			return nil
		}

		if err := o.PrintObj(podList, o.Out); err != nil {
			return err
		}

		if podList.Continue == "" {
			return nil
		}
		continueToken = podList.Continue
	}
}

// printPodTable is a printer function for PodList objects.
var _ printers.ResourcePrinterFunc = printPodTable

func printPodTable(obj runtime.Object, out io.Writer) error {
	tp := printers.NewTablePrinter(printers.PrintOptions{})
	a := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "Name", Type: "string", Format: "name"},
			{Name: "Status", Type: "string", Format: "status"},
			{Name: "Age", Type: "string"},
		},
		Rows: printPodList(obj.(*corev1.PodList)),
	}

	return tp.PrintObj(a, out)
}

func printPodList(list *corev1.PodList) []metav1.TableRow {
	rows := make([]metav1.TableRow, len(list.Items))
	for index := range list.Items {
		rows[index] = printPod(&list.Items[index])
	}
	return rows
}

func printPod(pod *corev1.Pod) metav1.TableRow {
	return metav1.TableRow{
		Object: runtime.RawExtension{Object: pod},
		Cells: []interface{}{
			pod.Name,
			pod.Status.Phase,
			duration.HumanDuration(time.Since(pod.CreationTimestamp.Time)),
		},
	}
}
