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
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	podLong = `Lists all pods that matches the given criteria: Should be part of the specified Job kind,
	belonging to the specified namespace, matching
	the label selector or the field selector.`
	podExample = `  # List Pods
  kueuectl list pods --for job/job-name`
)

type objectRef struct {
	APIGroup string
	Kind     string
	Name     string
}

type PodOptions struct {
	PrintFlags *genericclioptions.PrintFlags

	AllNamespaces          bool
	Namespace              string
	LabelSelector          string
	FieldSelector          string
	UserSpecifiedForObject string
	ForObject              objectRef

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
	addForObjectFilterFlagVar(cmd, &o.UserSpecifiedForObject)

	return cmd
}

// Complete takes the command arguments and infers any remaining options.
func (o *PodOptions) Complete(clientGetter util.ClientGetter) error {
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

	err = o.parseForObject()
	if err != nil {
		return err
	}

	return nil
}

// Run prints the pods for a specific Job
func (o *PodOptions) Run(ctx context.Context) error {
	err := o.listPods(ctx)
	if err != nil {
		return err
	}

	return nil
}

func (o *PodOptions) parseForObject() error {
	if o.UserSpecifiedForObject == "" {
		return nil
	}

	parts := strings.Split(o.UserSpecifiedForObject, "/")
	if len(parts) > 2 {
		return errors.New(fmt.Sprintf("invalid value '%s' used in --for flag; value must be in the format [TYPE[.API-GROUP]/]NAME", o.UserSpecifiedForObject))
	}

	if len(parts) == 1 {
		o.ForObject.Name = parts[0]
		return nil
	}

	o.ForObject.Name = parts[1]
	o.ForObject.Kind, o.ForObject.APIGroup, _ = strings.Cut(parts[0], ".")

	return nil
}

// listPods lists the pods based on the given --for object
func (o *PodOptions) listPods(ctx context.Context) error {
	continueToken := ""

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

		filteredPods := make([]corev1.Pod, 0, len(podList.Items))
		for i := range podList.Items {
			for _, ownerRef := range podList.Items[i].OwnerReferences {
				gv, _ := schema.ParseGroupVersion(ownerRef.APIVersion)

				if strings.EqualFold(o.ForObject.Kind, ownerRef.Kind) && strings.EqualFold(o.ForObject.Name, ownerRef.Name) {
					if o.ForObject.APIGroup == "" || strings.EqualFold(o.ForObject.APIGroup, gv.Group) {
						filteredPods = append(filteredPods, podList.Items[i])
						break
					}
				}
			}
		}

		// replace the podList items with the new filtered pods
		podList.Items = filteredPods

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
