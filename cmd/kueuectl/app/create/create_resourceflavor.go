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

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/kubectl/pkg/util/templates"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/client-go/clientset/versioned/scheme"
	kueuev1beta1 "sigs.k8s.io/kueue/client-go/clientset/versioned/typed/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/util"
)

var (
	rfLong    = templates.LongDesc(`Create a resource flavor with the given name.`)
	rfExample = templates.Examples(`  
		# Create a resource flavor 
  		kueuectl create resourceflavor my-resource-flavor

  		# Create a resource flavor with labels
		kueuectl create resourceflavor my-resource-flavor \
		--node-labels beta.kubernetes.io/arch=arm64,beta.kubernetes.io/os=linux

		# Create a resource flavor with node taints
  		kueuectl create resourceflavor my-resource-flavor \
		--node-taints key1=value:NoSchedule,key2:NoExecute

		# Create a resource flavor with tolerations
  		kueuectl create resourceflavor my-resource-flavor \
		--tolerations key1=value:NoSchedule,key2:NoExecute,key3=value,key4,:PreferNoSchedule
	`)
	nodeLabelsFlagName  = "node-labels"
	nodeTaintsFlagName  = "node-taints"
	tolerationsFlagName = "tolerations"
)

type ResourceFlavorOptions struct {
	PrintFlags *genericclioptions.PrintFlags

	DryRunStrategy util.DryRunStrategy
	Name           string
	NodeLabels     map[string]string
	NodeTaints     []corev1.Taint
	Tolerations    []corev1.Toleration

	UserSpecifiedNodeTaints  []string
	UserSpecifiedTolerations []string

	Client kueuev1beta1.KueueV1beta1Interface

	PrintObj printers.ResourcePrinterFunc

	genericiooptions.IOStreams
}

func NewResourceFlavorOptions(streams genericiooptions.IOStreams) *ResourceFlavorOptions {
	return &ResourceFlavorOptions{
		PrintFlags: genericclioptions.NewPrintFlags("created").WithTypeSetter(scheme.Scheme),
		IOStreams:  streams,
	}
}

func NewResourceFlavorCmd(clientGetter util.ClientGetter, streams genericiooptions.IOStreams) *cobra.Command {
	o := NewResourceFlavorOptions(streams)

	cmd := &cobra.Command{
		Use: "resourceflavor NAME " +
			"[--node-labels KEY=VALUE] " +
			"[--node-taints KEY[=VALUE]:EFFECT] " +
			"[--tolerations KEY[=VALUE][:EFFECT]]|:EFFECT " +
			"[--dry-run STRATEGY]",
		DisableFlagsInUseLine: true,
		Aliases:               []string{"rf"},
		Short:                 "Creates a resource flavor",
		Long:                  rfLong,
		Example:               rfExample,
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			err := o.Complete(clientGetter, cmd, args)
			if err != nil {
				return err
			}
			return o.Run(cmd.Context())
		},
	}

	o.PrintFlags.AddFlags(cmd)

	cmd.Flags().StringToStringVar(&o.NodeLabels, nodeLabelsFlagName, nil,
		"Labels that associate the ResourceFlavor with Nodes that have the same labels.")
	cmd.Flags().StringSliceVar(&o.UserSpecifiedNodeTaints, nodeTaintsFlagName, nil,
		"Taints that the nodes associated with this ResourceFlavor have.")
	cmd.Flags().StringSliceVar(&o.UserSpecifiedTolerations, tolerationsFlagName, nil,
		"Extra tolerations that will be added to the pods admitted in the quota associated with this resource flavor.")

	return cmd
}

// Complete completes all the required options
func (o *ResourceFlavorOptions) Complete(clientGetter util.ClientGetter, cmd *cobra.Command, args []string) error {
	o.Name = args[0]

	var err error

	o.NodeTaints, err = parseTaints(o.UserSpecifiedNodeTaints)
	if err != nil {
		return err
	}

	o.Tolerations, err = parseTolerations(o.UserSpecifiedTolerations)
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

// Run create a resource
func (o *ResourceFlavorOptions) Run(ctx context.Context) error {
	rf := o.createResourceFlavor()
	if o.DryRunStrategy != util.DryRunClient {
		var (
			createOptions metav1.CreateOptions
			err           error
		)
		if o.DryRunStrategy == util.DryRunServer {
			createOptions.DryRun = []string{metav1.DryRunAll}
		}
		rf, err = o.Client.ResourceFlavors().Create(ctx, rf, createOptions)
		if err != nil {
			return err
		}
	}
	return o.PrintObj(rf, o.Out)
}

func (o *ResourceFlavorOptions) createResourceFlavor() *v1beta1.ResourceFlavor {
	return &v1beta1.ResourceFlavor{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1beta1.SchemeGroupVersion.String(), Kind: "ResourceFlavor"},
		ObjectMeta: metav1.ObjectMeta{Name: o.Name},
		Spec: v1beta1.ResourceFlavorSpec{
			NodeLabels:  o.NodeLabels,
			NodeTaints:  o.NodeTaints,
			Tolerations: o.Tolerations,
		},
	}
}

// splitSpecItem split string by KEY[=VALUE][:EFFECT] template.
func splitSpecItem(str string) (string, string, string, error) {
	var key string
	var value string
	var effect string

	parts := strings.Split(str, ":")
	if len(parts) == 0 || len(parts) > 2 {
		return key, value, effect, errors.New("invalid spec")
	}

	partsKV := strings.Split(parts[0], "=")
	if len(partsKV) > 2 {
		return key, value, effect, errors.New("invalid spec")
	}

	if len(partsKV) > 0 {
		key = partsKV[0]
		if len(partsKV) > 1 {
			value = partsKV[1]
		}
	}

	if len(parts) > 1 {
		effect = parts[1]
	}

	return key, value, effect, nil
}

// parseTaints takes a spec which is an array and creates slices for new taints to be added.
// It also validates the spec.
func parseTaints(spec []string) ([]corev1.Taint, error) {
	var taints []corev1.Taint
	uniqueTaints := make(map[corev1.TaintEffect]sets.Set[string])

	for _, taintSpec := range spec {
		newTaint, err := parseTaint(taintSpec)
		if err != nil {
			return nil, err
		}
		// validate if taint is unique by <key, effect>
		if uniqueTaints[newTaint.Effect].Has(newTaint.Key) {
			return nil, fmt.Errorf("duplicated taints with the same key and effect: %v", newTaint)
		}
		// add taint to existingTaints for uniqueness check
		if len(uniqueTaints[newTaint.Effect]) == 0 {
			uniqueTaints[newTaint.Effect] = sets.New[string]()
		}
		uniqueTaints[newTaint.Effect].Insert(newTaint.Key)

		taints = append(taints, newTaint)
	}

	return taints, nil
}

// parseTaint parses a taint from a string, whose form must be either
// 'KEY=VALUE:EFFECT' or 'KEY:EFFECT'
func parseTaint(str string) (corev1.Taint, error) {
	var taint corev1.Taint

	if len(str) == 0 {
		return taint, errors.New("invalid taint spec")
	}

	key, value, effect, err := splitSpecItem(str)
	if err != nil {
		return taint, fmt.Errorf("invalid taint spec: %v", str)
	}

	if len(effect) == 0 {
		return taint, fmt.Errorf("invalid taint spec: %v", str)
	}

	taintEffect := corev1.TaintEffect(effect)
	if err := validateTaintEffect(taintEffect); err != nil {
		return taint, err
	}

	if errs := validation.IsQualifiedName(key); len(errs) > 0 {
		return taint, fmt.Errorf("invalid taint spec: %v, %s", str, strings.Join(errs, "; "))
	}

	if len(value) > 0 {
		if errs := validation.IsValidLabelValue(value); len(errs) > 0 {
			return taint, fmt.Errorf("invalid taint spec: %v, %s", str, strings.Join(errs, "; "))
		}
	}

	taint.Key = key
	taint.Value = value
	taint.Effect = taintEffect

	return taint, nil
}

func validateTaintEffect(effect corev1.TaintEffect) error {
	if effect != corev1.TaintEffectNoSchedule && effect != corev1.TaintEffectPreferNoSchedule && effect != corev1.TaintEffectNoExecute {
		return fmt.Errorf("invalid taint effect: %v, unsupported taint effect", effect)
	}
	return nil
}

// parseTolerations takes a spec which is an array and creates slices for new tolerations to be added.
// It also validates the spec.
func parseTolerations(spec []string) ([]corev1.Toleration, error) {
	var tolerations []corev1.Toleration

	for _, tolerationSpec := range spec {
		newTolerations, err := parseToleration(tolerationSpec)
		if err != nil {
			return nil, err
		}
		tolerations = append(tolerations, newTolerations)
	}

	return tolerations, nil
}

// parseToleration parses a toleration from a string, whose form must be either
// 'KEY=VALUE:EFFECT', 'KEY:EFFECT', 'KEY=VALUE', ':EFFECT' or 'KEY'
func parseToleration(str string) (corev1.Toleration, error) {
	var toleration corev1.Toleration

	if len(str) == 0 {
		return toleration, errors.New("invalid toleration spec")
	}

	key, value, effect, err := splitSpecItem(str)
	if err != nil {
		return toleration, fmt.Errorf("invalid toleration spec: %v", str)
	}

	if len(key) == 0 && len(value) > 0 {
		return toleration, fmt.Errorf("invalid toleration spec: %v", str)
	}

	taintEffect := corev1.TaintEffect(effect)
	if len(taintEffect) > 0 {
		if err := validateTaintEffect(taintEffect); err != nil {
			return toleration, err
		}
	}

	if len(key) > 0 {
		if errs := validation.IsQualifiedName(key); len(errs) > 0 {
			return toleration, fmt.Errorf("invalid toleration spec: %v, %s", str, strings.Join(errs, "; "))
		}
	}

	if len(value) > 0 {
		if errs := validation.IsValidLabelValue(value); len(errs) > 0 {
			return toleration, fmt.Errorf("invalid toleration spec: %v, %s", str, strings.Join(errs, "; "))
		}
	}

	toleration.Key = key
	if len(key) == 0 || len(value) == 0 {
		toleration.Operator = corev1.TolerationOpExists
	} else {
		toleration.Operator = corev1.TolerationOpEqual
	}
	toleration.Value = value
	toleration.Effect = taintEffect

	return toleration, nil
}
