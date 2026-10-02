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

package list

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/cli-runtime/pkg/resource"
	k8s "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	kubectlget "k8s.io/kubectl/pkg/cmd/get"
	"k8s.io/kubectl/pkg/util/templates"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/cmd/kueuectl/app/clientgetter"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/flags"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
)

var (
	podLong = templates.LongDesc(`
		Lists all pods that match the given criteria: should be part 
		of the specified Job kind, belonging to the specified namespace, 
		matching the label selector or the field selector.

		The --for=pod/pod-name option allows to find pods from the same 
		pod group as the specified pod, including that pod itself. 
	`)
	podExample = templates.Examples(`
		# List Pods for the Job
  		kueuectl list pods --for job/job-name

  		# List Pods for the Pod group
  		kueuectl list pods --for pod/pod-name
	`)
)

type PodOptions struct {
	PrintFlags *genericclioptions.PrintFlags

	Limit                  int64
	AllNamespaces          bool
	ServerPrint            bool
	Namespace              string
	LabelSelector          string
	FieldSelector          string
	UserSpecifiedForObject string
	ForName                string
	ForGVK                 schema.GroupVersionKind
	ForObject              *unstructured.Unstructured
	PodLabelSelector       string
	PodFieldSelector       string
	PodAnnotationSelector  *podAnnotationSelector
	IntegrationManager     *jobframework.IntegrationManager

	Clientset k8s.Interface

	genericiooptions.IOStreams
}

func NewPodOptions(streams genericiooptions.IOStreams) *PodOptions {
	return &PodOptions{
		PrintFlags:         genericclioptions.NewPrintFlags("").WithTypeSetter(scheme.Scheme),
		IntegrationManager: jobs.NewIntegrationManager(),
		IOStreams:          streams,
	}
}

func NewPodCmd(clientGetter clientgetter.ClientGetter, streams genericiooptions.IOStreams) *cobra.Command {
	o := NewPodOptions(streams)

	cmd := &cobra.Command{
		Use:                   "pods --for TYPE[.API-GROUP]/NAME",
		DisableFlagsInUseLine: true,
		Aliases:               []string{"po"},
		Short:                 "List Pods belong to a Job Kind",
		Long:                  podLong,
		Example:               podExample,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			err := o.Complete(clientGetter)
			if err != nil {
				return err
			}
			if o.ForObject == nil {
				return nil
			}
			if len(o.PodLabelSelector) == 0 && len(o.PodFieldSelector) == 0 && o.PodAnnotationSelector == nil {
				return fmt.Errorf("unsupported kind: %s", o.ForObject.GetKind())
			}
			return o.Run(clientGetter)
		},
	}

	o.PrintFlags.AddFlags(cmd)

	flags.AddAllNamespacesFlagVar(cmd, &o.AllNamespaces)
	addFieldSelectorFlagVar(cmd, &o.FieldSelector)
	addLabelSelectorFlagVar(cmd, &o.LabelSelector)
	addForObjectFlagVar(cmd, &o.UserSpecifiedForObject)

	_ = cmd.MarkFlagRequired("for")

	return cmd
}

// Complete takes the command arguments and infers any remaining options.
func (o *PodOptions) Complete(clientGetter clientgetter.ClientGetter) error {
	var err error

	o.Limit, err = listRequestLimit()
	if err != nil {
		return err
	}

	outputOption := ptr.Deref(o.PrintFlags.OutputFormat, "")
	if outputOption == "" || outputOption == "wide" {
		o.ServerPrint = true
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
		return fmt.Errorf("invalid value '%s' used in --for flag; value must be in the format TYPE[.API-GROUP]/NAME", o.UserSpecifiedForObject)
	}

	infos, err := o.getForObjectInfos(clientGetter)
	if err != nil {
		return err
	}

	if len(infos) == 0 {
		o.printNoResourcesFound()
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

	if err := o.completePodSelectors(); err != nil {
		return err
	}

	return nil
}

// completePodSelectors adjusts the selectors when --for points to a Pod. A pod group that
// keys its members by a label is already covered by getPodLabelSelector; the other two
// cases are not.
func (o *PodOptions) completePodSelectors() error {
	if o.ForGVK != corev1.SchemeGroupVersion.WithKind("Pod") {
		return nil
	}
	var pod corev1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.ForObject.UnstructuredContent(), &pod); err != nil {
		return fmt.Errorf("failed to convert unstructured object: %w", err)
	}

	if pod.Labels[podconstants.GroupNameLabel] != "" {
		return nil
	}

	// The group name may live in an annotation instead. It is read directly rather than
	// through utilpod.GetPodGroupName because that helper consults the
	// WorkloadIdentifierAnnotations feature gate, and this runs in the kueuectl binary,
	// whose gates are unrelated to those of the cluster that wrote the annotation.
	if groupName := pod.Annotations[podconstants.GroupNameAnnotation]; groupName != "" {
		// Label selectors cannot match annotations, so the members are listed without a
		// pod group selector and filtered client-side instead.
		o.PodLabelSelector = ""
		o.PodAnnotationSelector = &podAnnotationSelector{
			key:   podconstants.GroupNameAnnotation,
			value: groupName,
		}
		return nil
	}

	// A Pod without a group name has no label shared with other members,
	// so a label selector cannot find it. Select it by name instead.
	o.PodLabelSelector = ""
	o.PodFieldSelector = fmt.Sprintf("metadata.namespace=%s,metadata.name=%s", pod.Namespace, pod.Name)

	return nil
}

// podAnnotationSelector identifies the pods of a group whose name lives in an annotation,
// which the API server cannot select on.
type podAnnotationSelector struct {
	key   string
	value string
}

func (s *podAnnotationSelector) matches(annotations map[string]string) bool {
	return annotations[s.key] == s.value
}

// getForObjectInfos builds and executes a dynamic client query for a resource specified in --for
func (o *PodOptions) getForObjectInfos(clientGetter clientgetter.ClientGetter) ([]*resource.Info, error) {
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
		return nil, fmt.Errorf("building client for: %s", o.UserSpecifiedForObject)
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
		return nil, fmt.Errorf("unexpected type %T", infos[0].Object)
	}

	return job, nil
}

// getPodLabelSelector returns the podLabels used as a standard selector for jobs
func (o *PodOptions) getPodLabelSelector() (string, error) {
	cbs, ok := o.IntegrationManager.GetIntegrationByGVK(o.ForGVK)
	if !ok {
		return "", nil
	}

	if cbs.NewJob == nil {
		return "", nil
	}
	genericJob := cbs.NewJob()

	jobWithPodLabelSelector, ok := genericJob.(jobframework.JobWithPodLabelSelector)
	if !ok {
		return "", nil
	}

	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.ForObject.UnstructuredContent(), genericJob.Object()); err != nil {
		return "", fmt.Errorf("failed to convert unstructured object: %w", err)
	}

	return jobWithPodLabelSelector.PodLabelSelector(), nil
}

// joinSelectors joins non-empty selector requirements with commas.
func joinSelectors(selectors ...string) string {
	return strings.Join(slices.DeleteFunc(selectors, func(s string) bool { return s == "" }), ",")
}

type trackingWriterWrapper struct {
	Delegate io.Writer
	Written  int
}

func (t *trackingWriterWrapper) Write(p []byte) (n int, err error) {
	t.Written += len(p)
	return t.Delegate.Write(p)
}

// Run prints the pods for a specific Job
func (o *PodOptions) Run(clientGetter clientgetter.ClientGetter) error {
	trackingWriter := &trackingWriterWrapper{Delegate: o.Out}
	tabWriter := printers.GetNewTabWriter(trackingWriter)

	infos, err := o.getPodsInfos(clientGetter)
	if err != nil {
		return err
	}

	printer, err := o.ToPrinter()
	if err != nil {
		return err
	}

	if o.shouldPrintPodList() && len(infos) > 0 {
		podList, err := podListFromInfos(infos)
		if err != nil {
			return err
		}
		if err = printer.PrintObj(podList, tabWriter); err != nil {
			return err
		}
	} else {
		for _, pod := range infos {
			if err = printer.PrintObj(pod.Object, tabWriter); err != nil {
				return err
			}
		}
	}

	if err = tabWriter.Flush(); err != nil {
		return err
	}

	if trackingWriter.Written == 0 {
		o.printNoResourcesFound()
	}

	return nil
}

func (o *PodOptions) shouldPrintPodList() bool {
	outputFormat := ptr.Deref(o.PrintFlags.OutputFormat, "")
	return outputFormat == "json" || outputFormat == "yaml"
}

func podListFromInfos(infos []*resource.Info) (*unstructured.UnstructuredList, error) {
	podList := &unstructured.UnstructuredList{
		Items: make([]unstructured.Unstructured, len(infos)),
	}
	podList.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("PodList"))

	for i, info := range infos {
		pod, ok := info.Object.(*unstructured.Unstructured)
		if !ok {
			return nil, fmt.Errorf("unexpected type %T", info.Object)
		}
		podList.Items[i] = *pod
	}

	return podList, nil
}

func (o *PodOptions) ToPrinter() (printers.ResourcePrinterFunc, error) {
	if o.ServerPrint {
		tablePrinter := printers.NewTablePrinter(printers.PrintOptions{
			NoHeaders:     false,
			WithNamespace: o.AllNamespaces,
			WithKind:      false,
			Wide:          ptr.Deref(o.PrintFlags.OutputFormat, "") == "wide",
			ShowLabels:    false,
			ColumnLabels:  nil,
		})

		printer := &kubectlget.TablePrinter{Delegate: tablePrinter}

		return printer.PrintObj, nil
	}

	printer, err := o.PrintFlags.ToPrinter()
	if err != nil {
		return nil, err
	}

	return printer.PrintObj, nil
}

// getPodsInfos gets the pods raw infos directly from the API server
func (o *PodOptions) getPodsInfos(clientGetter clientgetter.ClientGetter) ([]*resource.Info, error) {
	namespace := o.Namespace
	if o.AllNamespaces {
		namespace = ""
	}

	r := clientGetter.NewResourceBuilder().Unstructured().
		NamespaceParam(namespace).DefaultNamespace().AllNamespaces(o.AllNamespaces).
		FieldSelectorParam(joinSelectors(o.FieldSelector, o.PodFieldSelector)).
		LabelSelectorParam(joinSelectors(o.LabelSelector, o.PodLabelSelector)).
		ResourceTypeOrNameArgs(true, "pods").
		ContinueOnError().
		RequestChunksOf(o.Limit).
		Latest().
		Flatten().
		TransformRequests(o.transformRequests).
		Do()

	if err := r.Err(); err != nil {
		return nil, err
	}

	infos, err := r.Infos()
	if err != nil {
		return nil, err
	}

	if o.PodAnnotationSelector != nil {
		return filterPodsByAnnotation(infos, o.PodAnnotationSelector)
	}

	return infos, nil
}

// filterPodsByAnnotation drops the pods that do not carry the selector's annotation.
//
// With server-side printing each info holds a Table whose rows embed the pod metadata, so
// the rows are filtered in place; otherwise each info holds a single pod.
func filterPodsByAnnotation(infos []*resource.Info, selector *podAnnotationSelector) ([]*resource.Info, error) {
	filtered := make([]*resource.Info, 0, len(infos))

	for _, info := range infos {
		obj, ok := info.Object.(*unstructured.Unstructured)
		if !ok {
			return nil, fmt.Errorf("unexpected type %T", info.Object)
		}

		if obj.GetKind() != "Table" {
			if selector.matches(obj.GetAnnotations()) {
				filtered = append(filtered, info)
			}
			continue
		}

		if err := filterTableRowsByAnnotation(obj, selector); err != nil {
			return nil, err
		}
		filtered = append(filtered, info)
	}

	return filtered, nil
}

func filterTableRowsByAnnotation(table *unstructured.Unstructured, selector *podAnnotationSelector) error {
	rows, found, err := unstructured.NestedSlice(table.Object, "rows")
	if err != nil {
		return fmt.Errorf("failed to read table rows: %w", err)
	}
	if !found {
		return nil
	}

	filtered := make([]any, 0, len(rows))
	for _, row := range rows {
		rowMap, ok := row.(map[string]any)
		if !ok {
			return fmt.Errorf("unexpected table row type %T", row)
		}
		annotations, _, err := unstructured.NestedStringMap(rowMap, "object", "metadata", "annotations")
		if err != nil {
			return fmt.Errorf("failed to read table row annotations: %w", err)
		}
		if selector.matches(annotations) {
			filtered = append(filtered, row)
		}
	}

	return unstructured.SetNestedSlice(table.Object, filtered, "rows")
}

func (o *PodOptions) transformRequests(req *rest.Request) {
	if !o.ServerPrint {
		return
	}
	req.SetHeader("Accept", strings.Join([]string{
		fmt.Sprintf("application/json;as=Table;v=%s;g=%s", metav1.SchemeGroupVersion.Version, metav1.GroupName),
		"application/json",
	}, ","))
}

// printNoResourcesFound handles output when there is no object found in any namespaces
func (o *PodOptions) printNoResourcesFound() {
	if !o.AllNamespaces {
		fmt.Fprintf(o.ErrOut, "No resources found in %s namespace.\n", o.Namespace)
	} else {
		fmt.Fprintln(o.ErrOut, "No resources found.")
	}
}
