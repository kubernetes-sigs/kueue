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

package describe

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/resource"
	"k8s.io/client-go/kubernetes"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/util"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
)

const (
	jobExample = `  # Describe a task with job mode
  kjobctl describe job sample-job

  # Describe a task with job mode
  kjobctl describe job/sample-job
  
  # Describe all tasks with job mode
  kjobctl describe job
  
  # Describe tasks by label name=myLabel
  kjobctl describe job -l name=myLabel`
)

const (
	modeTaskArgsFormat = iota
	modeSlashTaskArgsFormat
	modeArgsFormat
)

type DescribeOptions struct {
	AllNamespaces bool
	Namespace     string
	ProfileName   string
	ModeName      string
	TaskName      string
	LabelSelector string

	UserSpecifiedTask []string

	argsFormat      int
	ResourceGVK     schema.GroupVersionKind
	ResourceBuilder *resource.Builder

	Clientset kubernetes.Interface

	genericiooptions.IOStreams
}

func NewDescribeOptions(streams genericiooptions.IOStreams) *DescribeOptions {
	return &DescribeOptions{
		IOStreams: streams,
	}
}

func NewDescribeCmd(clientGetter util.ClientGetter, streams genericiooptions.IOStreams) *cobra.Command {
	o := NewDescribeOptions(streams)

	cmd := &cobra.Command{
		Use:                   "describe MODE NAME",
		DisableFlagsInUseLine: true,
		Short:                 "Show details of a specific resource or group of resources.",
		Example:               jobExample,
		Args:                  cobra.MatchAll(cobra.RangeArgs(1, 2), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			// stop the usage if we've got this far
			cmd.SilenceUsage = true

			err := o.Complete(clientGetter, args)
			if err != nil {
				return err
			}

			return o.Run(cmd.Context())
		},
	}

	util.AddAllNamespacesFlagVar(cmd, &o.AllNamespaces)
	util.AddLabelSelectorFlagVar(cmd, &o.LabelSelector)
	util.AddProfileFlagVar(cmd, &o.ProfileName)

	return cmd
}

func (o *DescribeOptions) Complete(clientGetter util.ClientGetter, args []string) error {
	var err error

	o.findArgsFormat(args)

	err = o.parseArgs(args)
	if err != nil {
		return err
	}

	resource := resourceFor(o.ModeName)
	mapper, err := clientGetter.ToRESTMapper()
	if err != nil {
		return err
	}
	o.ResourceGVK, err = gvkFor(mapper, resource)
	if err != nil {
		return err
	}

	o.Namespace, _, err = clientGetter.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return err
	}

	o.ResourceBuilder = clientGetter.NewResourceBuilder()

	o.Clientset, err = clientGetter.K8sClientset()
	if err != nil {
		return err
	}

	return nil
}

func (o *DescribeOptions) findArgsFormat(args []string) {
	if len(args) == 2 {
		o.argsFormat = modeTaskArgsFormat
	} else {
		if strings.Contains(args[0], "/") {
			o.argsFormat = modeSlashTaskArgsFormat
		} else {
			o.argsFormat = modeArgsFormat
		}
	}
}

func (o *DescribeOptions) parseArgs(args []string) error {
	switch o.argsFormat {
	case modeTaskArgsFormat:
		o.ModeName, o.TaskName = args[0], args[1]
	case modeSlashTaskArgsFormat:
		parsedArg, err := parseAppProfileModeName(args[0])
		if err != nil {
			return err
		}
		o.ModeName, o.TaskName = parsedArg[0], parsedArg[1]
	default:
		o.ModeName = args[0]
	}

	return nil
}

func (o *DescribeOptions) Run(ctx context.Context) error {
	builder := o.customizeResourceBuilder()

	r := builder.Do()
	if err := r.Err(); err != nil {
		return err
	}
	infos, err := r.Infos()
	if err != nil {
		return err
	}

	if strings.EqualFold(o.ModeName, string(v1alpha1.SlurmMode)) {
		configMapsAsInfos, err := o.getConfigMaps(ctx)
		if err != nil {
			return err
		}

		infos = append(infos, configMapsAsInfos...)
	}

	allErrs := []error{}
	errs := sets.NewString()
	first := true
	for _, info := range infos {
		obj, ok := info.Object.(*unstructured.Unstructured)
		if !ok {
			err := fmt.Errorf("invalid object %+v. Unexpected type %T", obj, info.Object)
			if errs.Has(err.Error()) {
				continue
			}
			allErrs = append(allErrs, err)
			errs.Insert(err.Error())
			continue
		}

		labels := obj.GetLabels()
		if _, ok := labels[constants.ProfileLabel]; !ok {
			continue
		}

		mapping := info.ResourceMapping()
		describer, err := NewResourceDescriber(mapping)
		if err != nil {
			if errs.Has(err.Error()) {
				continue
			}
			allErrs = append(allErrs, err)
			errs.Insert(err.Error())
			continue
		}

		output, err := describer.Describe(obj)
		if err != nil {
			if errs.Has(err.Error()) {
				continue
			}
			allErrs = append(allErrs, err)
			errs.Insert(err.Error())
			continue
		}

		if first {
			first = false
			fmt.Fprint(o.Out, output)
		} else {
			fmt.Fprintf(o.Out, "\n\n%s", output)
		}
	}

	if first && len(allErrs) == 0 {
		if o.AllNamespaces {
			fmt.Fprintln(o.ErrOut, "No resources found")
		} else {
			fmt.Fprintf(o.ErrOut, "No resources found in %s namespace.\n", o.Namespace)
		}
	}

	return errors.NewAggregate(allErrs)
}

func (o *DescribeOptions) customizeResourceBuilder() *resource.Builder {
	builder := o.ResourceBuilder.
		Unstructured().
		NamespaceParam(o.Namespace).
		DefaultNamespace()
	switch o.argsFormat {
	case modeTaskArgsFormat, modeSlashTaskArgsFormat:
		builder = builder.ResourceTypeOrNameArgs(true, o.ResourceGVK.Kind, o.TaskName).
			SingleResourceType()
	default:
		selector := constants.ProfileLabel
		if o.ProfileName != "" {
			selector += fmt.Sprintf("=%s", o.ProfileName)
		}
		if o.LabelSelector != "" {
			selector += fmt.Sprintf(",%s", o.LabelSelector)
		}

		builder = builder.AllNamespaces(o.AllNamespaces).
			LabelSelectorParam(selector).
			ResourceTypeOrNameArgs(true, o.ResourceGVK.Kind).
			ContinueOnError()
	}

	return builder.Flatten()
}

func (o *DescribeOptions) getConfigMaps(ctx context.Context) ([]*resource.Info, error) {
	infos := make([]*resource.Info, 0)

	if o.argsFormat == modeTaskArgsFormat || o.argsFormat == modeSlashTaskArgsFormat {
		cm, err := o.Clientset.CoreV1().ConfigMaps(o.Namespace).Get(ctx, o.TaskName, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}

		info, err := configMapToInfo(cm)
		if err != nil {
			return nil, err
		}

		infos = append(infos, info)
	} else {
		cmList, err := o.Clientset.CoreV1().ConfigMaps(o.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: constants.ProfileLabel,
		})
		if err != nil {
			return nil, err
		}

		for _, cm := range cmList.Items {
			info, err := configMapToInfo(&cm)
			if err != nil {
				return nil, err
			}

			infos = append(infos, info)
		}
	}

	return infos, nil
}

func configMapToInfo(cm *corev1.ConfigMap) (*resource.Info, error) {
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cm)
	if err != nil {
		return nil, err
	}

	obj := &unstructured.Unstructured{Object: u}
	return &resource.Info{
		Mapping: &meta.RESTMapping{
			GroupVersionKind: schema.GroupVersionKind{
				Version: "v1",
				Kind:    "ConfigMap",
			},
		},
		Object: obj,
	}, nil
}
