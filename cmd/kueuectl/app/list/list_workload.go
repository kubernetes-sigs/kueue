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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/kubectl/pkg/util/templates"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	visibility "sigs.k8s.io/kueue/apis/visibility/v1beta1"
	clientset "sigs.k8s.io/kueue/client-go/clientset/versioned"
	"sigs.k8s.io/kueue/client-go/clientset/versioned/scheme"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/completion"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/util"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	wlLong    = templates.LongDesc(`Lists Workloads that match the provided criteria.`)
	wlExample = templates.Examples(`
		# List Workload 
  		kueuectl list workload
	`)
)

const (
	workloadStatusAll = iota
	workloadStatusPending
	workloadStatusQuotaReserved
	workloadStatusAdmitted
	workloadStatusFinished
)

type WorkloadOptions struct {
	Clock      clock.Clock
	PrintFlags *genericclioptions.PrintFlags

	Limit              int64
	AllNamespaces      bool
	Namespace          string
	FieldSelector      string
	LabelSelector      string
	ClusterQueueFilter string
	LocalQueueFilter   string
	StatusesFilter     sets.Set[int]
	forGVK             schema.GroupVersionKind
	forName            string
	forObject          *unstructured.Unstructured

	UserSpecifiedForObject string

	ClientSet clientset.Interface

	genericiooptions.IOStreams
}

func NewWorkloadOptions(streams genericiooptions.IOStreams, clock clock.Clock) *WorkloadOptions {
	return &WorkloadOptions{
		PrintFlags: genericclioptions.NewPrintFlags("").WithTypeSetter(scheme.Scheme),
		IOStreams:  streams,
		Clock:      clock,
	}
}

func NewWorkloadCmd(clientGetter util.ClientGetter, streams genericiooptions.IOStreams, clock clock.Clock) *cobra.Command {
	o := NewWorkloadOptions(streams, clock)

	cmd := &cobra.Command{
		Use: "workload [--clusterqueue CLUSTER_QUEUE_NAME] [--localqueue LOCAL_QUEUE_NAME] [--status STATUS] [--selector key1=value1] [--field-selector key1=value1] [--all-namespaces] [--for TYPE[.API-GROUP]/NAME]",
		// To do not add "[flags]" suffix on the end of usage line
		DisableFlagsInUseLine: true,
		Aliases:               []string{"wl"},
		Short:                 "List Workload",
		Long:                  wlLong,
		Example:               wlExample,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			err := o.Complete(clientGetter, cmd)
			if err != nil {
				return err
			}
			return o.Run(cmd.Context())
		},
	}

	o.PrintFlags.AddFlags(cmd)

	util.AddAllNamespacesFlagVar(cmd, &o.AllNamespaces)
	addFieldSelectorFlagVar(cmd, &o.FieldSelector)
	addLabelSelectorFlagVar(cmd, &o.LabelSelector)
	addClusterQueueFilterFlagVar(cmd, &o.ClusterQueueFilter)
	addLocalQueueFilterFlagVar(cmd, &o.LocalQueueFilter)
	addForObjectFlagVar(cmd, &o.UserSpecifiedForObject)

	cobra.CheckErr(cmd.RegisterFlagCompletionFunc("clusterqueue", completion.ClusterQueueNameFunc(clientGetter, nil)))
	cobra.CheckErr(cmd.RegisterFlagCompletionFunc("localqueue", completion.LocalQueueNameFunc(clientGetter, nil)))

	cmd.Flags().StringArray("status", nil, `Filter workloads by status. Must be "all", "pending", "admitted" or "finished"`)

	return cmd
}

func getWorkloadStatuses(cmd *cobra.Command) (sets.Set[int], error) {
	statusesFlags, err := cmd.Flags().GetStringArray("status")
	if err != nil {
		return nil, err
	}

	statuses := sets.New[int]()

	for _, status := range statusesFlags {
		switch strings.ToLower(status) {
		case "all":
			statuses.Insert(workloadStatusAll)
		case "pending":
			statuses.Insert(workloadStatusPending)
		case "quotareserved":
			statuses.Insert(workloadStatusQuotaReserved)
		case "admitted":
			statuses.Insert(workloadStatusAdmitted)
		case "finished":
			statuses.Insert(workloadStatusFinished)
		default:
			return nil, fmt.Errorf(`Invalid status value (%v). Must be "all", "pending", "admitted" or "finished".`, status)
		}
	}

	return statuses, nil
}

// Complete completes all the required options
func (o *WorkloadOptions) Complete(clientGetter util.ClientGetter, cmd *cobra.Command) error {
	var err error

	o.Limit, err = listRequestLimit()
	if err != nil {
		return err
	}

	o.Namespace, _, err = clientGetter.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return err
	}

	o.StatusesFilter, err = getWorkloadStatuses(cmd)
	if err != nil {
		return err
	}

	o.ClientSet, err = clientGetter.KueueClientSet()
	if err != nil {
		return err
	}

	if o.UserSpecifiedForObject != "" {
		mapper, err := clientGetter.ToRESTMapper()
		if err != nil {
			return err
		}
		var found bool
		o.forGVK, o.forName, found, err = decodeResourceTypeName(mapper, o.UserSpecifiedForObject)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("--for must be in resource/name form")
		}

		r := clientGetter.NewResourceBuilder().
			Unstructured().
			NamespaceParam(o.Namespace).
			DefaultNamespace().
			AllNamespaces(o.AllNamespaces).
			FieldSelectorParam(fmt.Sprintf("metadata.name=%s", o.forName)).
			ResourceTypeOrNameArgs(true, o.forGVK.Kind).
			ContinueOnError().
			Latest().
			Flatten().
			Do()
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
		o.forObject = job
	}

	return nil
}

func (o *WorkloadOptions) ToPrinter(r *listWorkloadResources, headers bool) (printers.ResourcePrinterFunc, error) {
	if !o.PrintFlags.OutputFlagSpecified() {
		printer := newWorkloadTablePrinter().
			WithResources(r).
			WithNamespace(o.AllNamespaces).
			WithHeaders(headers).
			WithClock(o.Clock)
		return printer.PrintObj, nil
	}
	printer, err := o.PrintFlags.ToPrinter()
	if err != nil {
		return nil, err
	}
	return printer.PrintObj, nil
}

// Run performs the list operation.
func (o *WorkloadOptions) Run(ctx context.Context) error {
	var totalCount int

	namespace := o.Namespace
	if o.AllNamespaces {
		namespace = ""
	}

	var jobUID types.UID
	var jobUIDLabelSelector string
	if o.forObject != nil {
		jobUID = o.forObject.GetUID()

		if len(o.LabelSelector) != 0 {
			jobUIDLabelSelector += ","
		}
		jobUIDLabelSelector += fmt.Sprintf("%s=%s", constants.JobUIDLabel, jobUID)
	}

	opts := metav1.ListOptions{
		LabelSelector: o.LabelSelector + jobUIDLabelSelector,
		FieldSelector: o.FieldSelector,
		Limit:         o.Limit,
	}

	tabWriter := printers.GetNewTabWriter(o.Out)

	var enableOwnerReferenceFilter bool
	for {
		headers := totalCount == 0

		list, err := o.ClientSet.KueueV1beta1().Workloads(namespace).List(ctx, opts)
		if err != nil {
			return err
		}

		if o.forObject != nil && len(list.Items) == 0 && list.Continue == "" && strings.Contains(opts.LabelSelector, jobUIDLabelSelector) {
			opts.LabelSelector = o.LabelSelector
			enableOwnerReferenceFilter = true
			continue
		}

		o.filterList(list, enableOwnerReferenceFilter, jobUID)

		totalCount += len(list.Items)

		r := newListWorkloadResources()

		r.localQueues, err = o.localQueues(ctx, list)
		if err != nil {
			return err
		}

		r.pendingWorkloads, err = o.pendingWorkloads(ctx, list, r.localQueues)
		if err != nil {
			return err
		}

		r.apiResourceLists, err = o.apiResources(list)
		if err != nil {
			return err
		}

		printer, err := o.ToPrinter(r, headers)
		if err != nil {
			return err
		}

		if err := printer.PrintObj(list, tabWriter); err != nil {
			return err
		}

		if list.Continue != "" {
			opts.Continue = list.Continue
			continue
		}

		if totalCount == 0 {
			if !o.AllNamespaces {
				fmt.Fprintf(o.ErrOut, "No resources found in %s namespace.\n", o.Namespace)
			} else {
				fmt.Fprintln(o.ErrOut, "No resources found")
			}
			return nil
		}

		if err := tabWriter.Flush(); err != nil {
			return err
		}

		return nil
	}
}

func (o *WorkloadOptions) filterList(list *v1beta1.WorkloadList, enableOwnerReferenceFilter bool, uid types.UID) {
	if len(list.Items) == 0 {
		return
	}
	filteredItems := make([]v1beta1.Workload, 0, len(o.LocalQueueFilter))
	for _, wl := range list.Items {
		if o.filterByLocalQueue(&wl) && o.filterByClusterQueue(&wl) && o.filterByStatuses(&wl) &&
			o.filterByOwnerReference(&wl, enableOwnerReferenceFilter, uid) {
			filteredItems = append(filteredItems, wl)
		}
	}
	list.Items = filteredItems
}

func (o *WorkloadOptions) filterByLocalQueue(wl *v1beta1.Workload) bool {
	return len(o.LocalQueueFilter) == 0 || wl.Spec.QueueName == o.LocalQueueFilter
}

func (o *WorkloadOptions) filterByClusterQueue(wl *v1beta1.Workload) bool {
	return len(o.ClusterQueueFilter) == 0 || wl.Status.Admission != nil &&
		wl.Status.Admission.ClusterQueue == v1beta1.ClusterQueueReference(o.ClusterQueueFilter)
}

func (o *WorkloadOptions) filterByStatuses(wl *v1beta1.Workload) bool {
	if o.StatusesFilter.Len() == 0 || o.StatusesFilter.Has(workloadStatusAll) {
		return true
	}

	status := workload.Status(wl)

	if o.StatusesFilter.Has(workloadStatusPending) && status == workload.StatusPending {
		return true
	}

	if o.StatusesFilter.Has(workloadStatusAdmitted) && status == workload.StatusAdmitted {
		return true
	}

	if o.StatusesFilter.Has(workloadStatusQuotaReserved) && status == workload.StatusQuotaReserved {
		return true
	}

	if o.StatusesFilter.Has(workloadStatusFinished) && status == workload.StatusFinished {
		return true
	}

	return false
}

func (o *WorkloadOptions) filterByOwnerReference(wl *v1beta1.Workload, isEnabled bool, uid types.UID) bool {
	if !isEnabled {
		return true
	}

	for _, ow := range wl.OwnerReferences {
		if ow.UID == uid {
			return true
		}
	}

	return false
}

func (o *WorkloadOptions) localQueues(ctx context.Context, list *v1beta1.WorkloadList) (map[string]*v1beta1.LocalQueue, error) {
	localQueues := make(map[string]*v1beta1.LocalQueue)
	for _, wl := range list.Items {
		// It's not necessary to get localqueue if we have clusterqueue name on Admission status.
		if wl.Status.Admission != nil && len(wl.Status.Admission.ClusterQueue) > 0 {
			continue
		}
		if _, ok := localQueues[localQueueKeyForWorkload(&wl)]; !ok {
			lq, err := o.ClientSet.KueueV1beta1().LocalQueues(wl.Namespace).Get(ctx, wl.Spec.QueueName, metav1.GetOptions{})
			if client.IgnoreNotFound(err) != nil {
				return nil, err
			}
			if err != nil {
				lq = nil
			}
			localQueues[localQueueKeyForWorkload(&wl)] = lq
		}
	}
	return localQueues, nil
}

func (o *WorkloadOptions) pendingWorkloads(ctx context.Context, list *v1beta1.WorkloadList, localQueues map[string]*v1beta1.LocalQueue) (map[string]*visibility.PendingWorkload, error) {
	var err error

	pendingWorkloads := make(map[string]*visibility.PendingWorkload)
	pendingWorkloadsSummaries := make(map[string]*visibility.PendingWorkloadsSummary)

	for _, wl := range list.Items {
		if !workloadPending(&wl) {
			continue
		}
		var clusterQueueName string
		if wl.Status.Admission != nil && len(wl.Status.Admission.ClusterQueue) > 0 {
			clusterQueueName = string(wl.Status.Admission.ClusterQueue)
		} else if lq := localQueues[localQueueKeyForWorkload(&wl)]; lq != nil {
			clusterQueueName = string(lq.Spec.ClusterQueue)
		}
		if len(clusterQueueName) == 0 {
			continue
		}
		pendingWorkloadsSummary, ok := pendingWorkloadsSummaries[clusterQueueName]
		if !ok {
			pendingWorkloadsSummary, err = o.ClientSet.VisibilityV1beta1().ClusterQueues().
				GetPendingWorkloadsSummary(ctx, clusterQueueName, metav1.GetOptions{})
			if client.IgnoreNotFound(err) != nil {
				return nil, err
			}
			if err != nil {
				pendingWorkloadsSummary = nil
			}
			pendingWorkloadsSummaries[clusterQueueName] = pendingWorkloadsSummary
		}
		if pendingWorkloadsSummary == nil {
			continue
		}
		for _, pendingWorkload := range pendingWorkloadsSummary.Items {
			if pendingWorkload.Name == wl.Name && pendingWorkload.Namespace == wl.Namespace {
				pendingWorkloads[workload.Key(&wl)] = &pendingWorkload
			}
		}
	}

	return pendingWorkloads, nil
}

func (o *WorkloadOptions) apiResources(list *v1beta1.WorkloadList) (map[string]*metav1.APIResourceList, error) {
	apiResourceLists := make(map[string]*metav1.APIResourceList)
	for _, wl := range list.Items {
		for _, ref := range wl.ObjectMeta.OwnerReferences {
			if _, ok := apiResourceLists[ref.APIVersion]; !ok {
				rl, err := o.ClientSet.Discovery().ServerResourcesForGroupVersion(ref.APIVersion)
				if client.IgnoreNotFound(err) != nil {
					return nil, err
				}
				if err != nil {
					rl = nil
				}
				apiResourceLists[ref.APIVersion] = rl
			}
		}
	}
	return apiResourceLists, nil
}

func workloadPending(wl *v1beta1.Workload) bool {
	return workload.Status(wl) == workload.StatusPending
}

func localQueueKeyForWorkload(wl *v1beta1.Workload) string {
	return fmt.Sprintf("%s/%s", wl.Namespace, wl.Spec.QueueName)
}
