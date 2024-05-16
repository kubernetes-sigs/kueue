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
	"slices"
	"strings"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/client-go/clientset/versioned/scheme"
	kueuev1beta1 "sigs.k8s.io/kueue/client-go/clientset/versioned/typed/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/util"
)

const (
	cqLong    = `Creates a ClusterQueue with the given name.`
	cqExample = `  # Create a ClusterQueue 
  kueuectl create clusterqueue my-cluster-queue
  
  # Create a ClusterQueue with cohort, namespace selector and other details
  kueuectl create clusterqueue my-cluster-queue \
  	--cohort=cohortname \
	--queuing-strategy=StrictFIFO \
	--namespace-selector=fooX=barX,fooY=barY \
	--reclaim-within-cohort=Any \
	--preemption-within-cluster-queue=LowerPriority
  
  # Create a ClusterQueue with nominal quota and one resource flavor named alpha
  kueuectl create clusterqueue my-cluster-queue --nominal-quota=alpha:cpu=0;memory=0
  
  # Create a ClusterQueue multiple resource flavors named alpha, beta and gamma
  kueuectl create clusterqueue my-cluster-queue \
  	--nominal-quota=alpha:cpu=0;memory=0,beta:gpu=0,gamma:cpu=0;memory=0 \
	--borrowing-limit=alpha:cpu=0;memory=0,beta:gpu=0,gamma:cpu=0;memory=0 \
	--lending-limit=alpha:cpu=0;memory=0,beta:gpu=0,gamma:cpu=0;memory=0`
	cohort                       = "cohort"
	queuingStrategy              = "queuing-strategy"
	namespaceSelector            = "namespace-selector"
	reclaimWithinCohort          = "reclaim-within-cohort"
	preemptionWithinClusterQueue = "preemption-within-cluster-queue"
	nominalQuota                 = "nominal-quota"
	borrowingLimit               = "borrowing-limit"
	lendingLimit                 = "lending-limit"
)

var (
	errResourceQuotaNotFound = errors.New("resource quota not found")
	errInvalidFlavor         = errors.New("invalid flavor")
	errInvalidResourceGroup  = errors.New("invalid resource group")
)

type ClusterQueueOptions struct {
	PrintFlags *genericclioptions.PrintFlags

	DryRunStrategy               util.DryRunStrategy
	Name                         string
	Cohort                       string
	QueueingStrategy             v1beta1.QueueingStrategy
	NamespaceSelector            metav1.LabelSelector
	ReclaimWithinCohort          v1beta1.PreemptionPolicy
	PreemptionWithinClusterQueue v1beta1.PreemptionPolicy
	ResourceGroups               []v1beta1.ResourceGroup

	UserSpecifiedQueueingStrategy             string
	UserSpecifiedNamespaceSelector            map[string]string
	UserSpecifiedReclaimWithinCohort          string
	UserSpecifiedPreemptionWithinClusterQueue string
	UserSpecifiedNominalQuota                 []string
	UserSpecifiedBorrowingLimit               []string
	UserSpecifiedLendingLimit                 []string

	Client kueuev1beta1.KueueV1beta1Interface

	PrintObj printers.ResourcePrinterFunc

	genericiooptions.IOStreams
}

func NewClusterQueueOptions(streams genericiooptions.IOStreams) *ClusterQueueOptions {
	return &ClusterQueueOptions{
		PrintFlags:                   genericclioptions.NewPrintFlags("created").WithTypeSetter(scheme.Scheme),
		IOStreams:                    streams,
		QueueingStrategy:             v1beta1.BestEffortFIFO,
		ReclaimWithinCohort:          v1beta1.PreemptionPolicyNever,
		PreemptionWithinClusterQueue: v1beta1.PreemptionPolicyNever,
	}
}

func NewClusterQueueCmd(clientGetter util.ClientGetter, streams genericiooptions.IOStreams) *cobra.Command {
	o := NewClusterQueueOptions(streams)

	cmd := &cobra.Command{
		Use: "clusterqueue NAME " +
			"[--cohort cohortname] " +
			"[--queuing-strategy StrictFIFO|BestEffortFIFO] " +
			"[--namespace-selector selector] " +
			"[--reclaim-within-cohort Never|Any|LowerPriority|LowerOrNewerEqualPriority] " +
			"[--preemption-within-cluster-queue Never|Any|LowerPriority|LowerOrNewerEqualPriority] " +
			"[--nominal-quota rfname1:resource1=value;resource2=value;resource3=value,rfname2:resource1=value;resource2=value] " +
			"[--borrowing-limit rfname1:resource1=value;resource2=value;resource3=value,rfname2:resource1=value;resource2=value] " +
			"[--lending-limit rfname1:resource1=value;resource2=value;resource3=value,rfname2:resource1=value;resource2=value] " +
			"[--dry-run server|client|none]",
		// To do not add "[flags]" suffix on the end of usage line
		DisableFlagsInUseLine: true,
		Aliases:               []string{"cq"},
		Short:                 "Creates a clusterqueue",
		Long:                  cqLong,
		Example:               cqExample,
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		Run: func(cmd *cobra.Command, args []string) {
			ctx := cmd.Context()
			cobra.CheckErr(o.Complete(clientGetter, cmd, args))
			cobra.CheckErr(o.Validate(ctx))
			cobra.CheckErr(o.Run(ctx))
		},
	}

	o.PrintFlags.AddFlags(cmd)

	cmd.Flags().StringVar(&o.Cohort, cohort, o.Cohort,
		"The cohort that this ClusterQueue belongs to.")
	cmd.Flags().StringVar(&o.UserSpecifiedQueueingStrategy, queuingStrategy, "",
		"The queueing strategy of the workloads across the queues in this ClusterQueue.")
	cmd.Flags().StringToStringVar(&o.UserSpecifiedNamespaceSelector, namespaceSelector, map[string]string{},
		"Defines which namespaces are allowed to submit workloads to this clusterQueue.")
	cmd.Flags().StringVar(&o.UserSpecifiedReclaimWithinCohort, reclaimWithinCohort, "",
		"Determines whether a pending Workload can preempt Workloads from other ClusterQueues in the cohort that are using more than their nominal quota.")
	cmd.Flags().StringVar(&o.UserSpecifiedPreemptionWithinClusterQueue, preemptionWithinClusterQueue, "",
		"Determines whether a pending Workload that doesn't fit within the nominal quota for its ClusterQueue, can preempt active Workloads in the ClusterQueue.")
	cmd.Flags().StringSliceVar(&o.UserSpecifiedNominalQuota, nominalQuota, []string{},
		"The quantity of this resource that is available for Workloads admitted by this ClusterQueue at a point in time.")
	cmd.Flags().StringSliceVar(&o.UserSpecifiedBorrowingLimit, borrowingLimit, []string{},
		"The maximum amount of quota for the [flavor, resource] combination that this ClusterQueue is allowed to borrow from the unused quota of other ClusterQueues in the same cohort.")
	cmd.Flags().StringSliceVar(&o.UserSpecifiedLendingLimit, lendingLimit, []string{},
		"The maximum amount of unused quota for the [flavor, resource] combination that this ClusterQueue can lend to other ClusterQueues in the same cohort.")

	return cmd
}

// Complete completes all the required options
func (o *ClusterQueueOptions) Complete(clientGetter util.ClientGetter, cmd *cobra.Command, args []string) error {
	o.Name = args[0]

	if cmd.Flags().Changed(queuingStrategy) {
		o.QueueingStrategy = v1beta1.QueueingStrategy(o.UserSpecifiedQueueingStrategy)
	}

	if cmd.Flags().Changed(namespaceSelector) {
		o.NamespaceSelector = metav1.LabelSelector{
			MatchLabels: o.UserSpecifiedNamespaceSelector,
		}
	}

	if cmd.Flags().Changed(reclaimWithinCohort) {
		o.ReclaimWithinCohort = v1beta1.PreemptionPolicy(o.UserSpecifiedReclaimWithinCohort)
	}

	if cmd.Flags().Changed(preemptionWithinClusterQueue) {
		o.PreemptionWithinClusterQueue = v1beta1.PreemptionPolicy(o.UserSpecifiedPreemptionWithinClusterQueue)
	}

	var err error
	if cmd.Flags().Changed(nominalQuota) || cmd.Flags().Changed(borrowingLimit) || cmd.Flags().Changed(lendingLimit) {
		err = o.parseResourceGroups()
	}
	if err != nil {
		return err
	}

	clientset, err := clientGetter.KueueClientSet()
	if err != nil {
		return err
	}

	o.Client = clientset.KueueV1beta1()

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

// Validate validates required fields are set to support structured generation
func (o *ClusterQueueOptions) Validate(ctx context.Context) error {
	if len(o.Name) == 0 {
		return fmt.Errorf("name must be specified")
	}

	return nil
}

// Run create clusterqueue
func (o *ClusterQueueOptions) Run(ctx context.Context) error {
	cq := o.createClusterQueue()
	if o.DryRunStrategy != util.DryRunClient {
		var (
			createOptions metav1.CreateOptions
			err           error
		)
		if o.DryRunStrategy == util.DryRunServer {
			createOptions.DryRun = []string{metav1.DryRunAll}
		}
		cq, err = o.Client.ClusterQueues().Create(ctx, cq, createOptions)
		if err != nil {
			return err
		}
	}
	return o.PrintObj(cq, o.Out)
}

func (o *ClusterQueueOptions) createClusterQueue() *v1beta1.ClusterQueue {
	return &v1beta1.ClusterQueue{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1beta1.SchemeGroupVersion.String(), Kind: "ClusterQueue"},
		ObjectMeta: metav1.ObjectMeta{Name: o.Name},
		Spec: v1beta1.ClusterQueueSpec{
			Cohort:            o.Cohort,
			QueueingStrategy:  o.QueueingStrategy,
			NamespaceSelector: &o.NamespaceSelector,
			Preemption: &v1beta1.ClusterQueuePreemption{
				ReclaimWithinCohort: o.ReclaimWithinCohort,
				WithinClusterQueue:  o.PreemptionWithinClusterQueue,
			},
			ResourceGroups: o.ResourceGroups,
		},
	}
}

func (o *ClusterQueueOptions) parseResourceGroups() error {
	nominalQuotas := parseUserSpecifiedResourceQuotas(o.UserSpecifiedNominalQuota, nominalQuota)
	borrowingLimits := parseUserSpecifiedResourceQuotas(o.UserSpecifiedBorrowingLimit, borrowingLimit)
	lendingLimits := parseUserSpecifiedResourceQuotas(o.UserSpecifiedLendingLimit, lendingLimit)

	resourceGroups, err := mergeResourcesByFlavor(slices.Concat(nominalQuotas, borrowingLimits, lendingLimits))
	if err != nil {
		return err
	}

	resourceGroups, err = mergeFlavorsByCoveredResources(resourceGroups)
	if err != nil {
		return err
	}

	o.ResourceGroups = resourceGroups

	return nil
}

func parseUserSpecifiedResourceQuotas(resources []string, quotaType string) []v1beta1.ResourceGroup {
	var resourceGroups []v1beta1.ResourceGroup
	for _, r := range resources {
		resourceGroups = append(resourceGroups, toResourceGroup(r, quotaType))
	}

	return resourceGroups
}

func toResourceGroup(spec, quotaType string) v1beta1.ResourceGroup {
	flavorName, userSpecifiedResources := parseKeyValue(spec, ":")
	resourceSpecs := strings.Split(userSpecifiedResources, ";")

	return v1beta1.ResourceGroup{
		CoveredResources: getCoveredResources(resourceSpecs),
		Flavors: []v1beta1.FlavorQuotas{
			toFlavorQuotas(flavorName, resourceSpecs, quotaType),
		},
	}
}

func getCoveredResources(resourceSpecs []string) []corev1.ResourceName {
	coveredResources := make([]corev1.ResourceName, 0, len(resourceSpecs))
	for _, spec := range resourceSpecs {
		resourceName, _ := parseKeyValue(spec, "=")
		coveredResources = append(coveredResources, corev1.ResourceName(resourceName))
	}

	return coveredResources
}

func toFlavorQuotas(name string, resourceSpecs []string, quotaType string) v1beta1.FlavorQuotas {
	resourceQuotas := make([]v1beta1.ResourceQuota, 0, len(resourceSpecs))
	for _, spec := range resourceSpecs {
		resourceQuotas = append(resourceQuotas, toResourceQuota(spec, quotaType))
	}

	return v1beta1.FlavorQuotas{
		Name:      v1beta1.ResourceFlavorReference(name),
		Resources: resourceQuotas,
	}
}

func toResourceQuota(spec, quotaType string) v1beta1.ResourceQuota {
	name, quota := parseKeyValue(spec, "=")

	rq := v1beta1.ResourceQuota{
		Name: corev1.ResourceName(name),
	}

	quantity := resource.MustParse(quota)
	switch quotaType {
	case nominalQuota:
		rq.NominalQuota = quantity
	case borrowingLimit:
		rq.BorrowingLimit = ptr.To(quantity)
	case lendingLimit:
		rq.LendingLimit = ptr.To(quantity)
	}

	return rq
}

func parseKeyValue(str, sep string) (string, string) {
	pair := strings.SplitN(str, sep, 2)
	if len(pair) == 1 {
		return strings.TrimSpace(pair[0]), ""
	}
	return strings.TrimSpace(pair[0]), strings.TrimSpace(pair[1])
}

func mergeResourcesByFlavor(resourceGroups []v1beta1.ResourceGroup) ([]v1beta1.ResourceGroup, error) {
	var mergedResources []v1beta1.ResourceGroup

	indexByFlavor := make(map[string]int)
	var index int
	for _, rg := range resourceGroups {
		flavorName := string(rg.Flavors[0].Name)
		idx, found := indexByFlavor[flavorName]
		if !found {
			mergedResources = append(mergedResources, rg)
			indexByFlavor[flavorName] = index
			index++
			continue
		}

		var err error
		mergedResources[idx].Flavors[0].Resources, err = mergeResourceQuotas(mergedResources[idx].Flavors[0].Resources, rg.Flavors[0].Resources)
		if err != nil {
			// multiple FlavorQuotas with same name have been found but resources listed don't match
			return mergedResources, errInvalidFlavor
		}
	}

	return mergedResources, nil
}

func mergeResourceQuotas(rQuotas1, rQuotas2 []v1beta1.ResourceQuota) ([]v1beta1.ResourceQuota, error) {
	var mergedResourceQuotas []v1beta1.ResourceQuota

	for _, rq1 := range rQuotas1 {
		idx := slices.IndexFunc(rQuotas2, func(rq v1beta1.ResourceQuota) bool { return rq.Name == rq1.Name })
		if idx == -1 {
			// both ResourceQuota lists should contain exactly the same resource names
			return mergedResourceQuotas, errResourceQuotaNotFound
		}

		rq2 := rQuotas2[idx]
		if rq1.NominalQuota.IsZero() {
			rq1.NominalQuota = rq2.NominalQuota
		}
		if rq1.BorrowingLimit == nil {
			rq1.BorrowingLimit = rq2.BorrowingLimit
		}
		if rq1.LendingLimit == nil {
			rq1.LendingLimit = rq2.LendingLimit
		}

		mergedResourceQuotas = append(mergedResourceQuotas, rq1)
	}

	return mergedResourceQuotas, nil
}

func mergeFlavorsByCoveredResources(resourceGroups []v1beta1.ResourceGroup) ([]v1beta1.ResourceGroup, error) {
	var mergedResources []v1beta1.ResourceGroup

	indexByResourceGroupID := make(map[string]int)
	var index int
	for _, rg := range resourceGroups {
		resourcesGroupID := getResourcesGroupID(rg.CoveredResources)
		if idx, found := indexByResourceGroupID[resourcesGroupID]; found {
			mergedResources[idx].Flavors = append(mergedResources[idx].Flavors, rg.Flavors...)
			continue
		}

		if !isResourceGroupValid(indexByResourceGroupID, resourcesGroupID) {
			return mergedResources, errInvalidResourceGroup
		}
		mergedResources = append(mergedResources, rg)
		indexByResourceGroupID[resourcesGroupID] = index
		index++
	}

	return mergedResources, nil
}

func getResourcesGroupID(coveredResources []corev1.ResourceName) string {
	var s []string
	for _, cr := range coveredResources {
		s = append(s, string(cr))
	}
	slices.Sort(s)

	return strings.Join(s, ".")
}

func isResourceGroupValid(indexByResourceGroup map[string]int, newResourceGroup string) bool {
	// check that new resource groups dooesn't share resources with another group
	for k := range indexByResourceGroup {
		if strings.Contains(k, newResourceGroup) {
			return false
		}
	}

	return true
}
