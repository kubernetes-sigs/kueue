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

package completion

import (
	"slices"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/util"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
)

const completionLimit = 100

func NamespaceNameFunc(clientGetter util.ClientGetter) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		clientSet, err := clientGetter.K8sClientset()
		if err != nil {
			return []string{}, cobra.ShellCompDirectiveError
		}

		list, err := clientSet.CoreV1().Namespaces().List(cmd.Context(), metav1.ListOptions{Limit: completionLimit})
		if err != nil {
			return []string{}, cobra.ShellCompDirectiveError
		}

		validArgs := make([]string, len(list.Items))
		for i, wl := range list.Items {
			validArgs[i] = wl.Name
		}

		return validArgs, cobra.ShellCompDirectiveNoFileComp
	}
}

func ContextsFunc(clientGetter util.ClientGetter) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		config, err := clientGetter.ToRawKubeConfigLoader().RawConfig()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var validArgs []string
		for name := range config.Contexts {
			if strings.HasPrefix(name, toComplete) {
				validArgs = append(validArgs, name)
			}
		}
		return validArgs, cobra.ShellCompDirectiveNoFileComp
	}
}

func ClustersFunc(clientGetter util.ClientGetter) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		config, err := clientGetter.ToRawKubeConfigLoader().RawConfig()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var validArgs []string
		for name := range config.Clusters {
			if strings.HasPrefix(name, toComplete) {
				validArgs = append(validArgs, name)
			}
		}
		return validArgs, cobra.ShellCompDirectiveNoFileComp
	}
}

func UsersFunc(clientGetter util.ClientGetter) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		config, err := clientGetter.ToRawKubeConfigLoader().RawConfig()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var validArgs []string
		for name := range config.AuthInfos {
			if strings.HasPrefix(name, toComplete) {
				validArgs = append(validArgs, name)
			}
		}
		return validArgs, cobra.ShellCompDirectiveNoFileComp
	}
}

func ApplicationProfileNameFunc(clientGetter util.ClientGetter) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		clientSet, err := clientGetter.KjobctlClientset()
		if err != nil {
			return []string{}, cobra.ShellCompDirectiveError
		}

		namespace, _, err := clientGetter.ToRawKubeConfigLoader().Namespace()
		if err != nil {
			return []string{}, cobra.ShellCompDirectiveError
		}

		list, err := clientSet.KjobctlV1alpha1().ApplicationProfiles(namespace).List(cmd.Context(), metav1.ListOptions{Limit: completionLimit})
		if err != nil {
			return []string{}, cobra.ShellCompDirectiveError
		}

		validArgs := make([]string, len(list.Items))
		for i, wl := range list.Items {
			validArgs[i] = wl.Name
		}

		return validArgs, cobra.ShellCompDirectiveNoFileComp
	}
}

func LocalQueueNameFunc(clientGetter util.ClientGetter) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		clientSet, err := clientGetter.KueueClientset()
		if err != nil {
			return []string{}, cobra.ShellCompDirectiveError
		}

		namespace, _, err := clientGetter.ToRawKubeConfigLoader().Namespace()
		if err != nil {
			return []string{}, cobra.ShellCompDirectiveError
		}

		list, err := clientSet.KueueV1beta1().LocalQueues(namespace).List(cmd.Context(), metav1.ListOptions{Limit: completionLimit})
		if err != nil {
			return []string{}, cobra.ShellCompDirectiveError
		}

		validArgs := make([]string, len(list.Items))
		for i, wl := range list.Items {
			validArgs[i] = wl.Name
		}

		return validArgs, cobra.ShellCompDirectiveNoFileComp
	}
}

func JobNameFunc(clientGetter util.ClientGetter) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		clientset, err := clientGetter.K8sClientset()
		if err != nil {
			return []string{}, cobra.ShellCompDirectiveError
		}

		namespace, _, err := clientGetter.ToRawKubeConfigLoader().Namespace()
		if err != nil {
			return []string{}, cobra.ShellCompDirectiveError
		}

		opts := metav1.ListOptions{LabelSelector: constants.ProfileLabel, Limit: completionLimit}
		list, err := clientset.BatchV1().Jobs(namespace).List(cmd.Context(), opts)
		if err != nil {
			return []string{}, cobra.ShellCompDirectiveError
		}

		var validArgs []string
		for _, job := range list.Items {
			if !slices.Contains(args, job.Name) {
				validArgs = append(validArgs, job.Name)
			}
		}

		return validArgs, cobra.ShellCompDirectiveNoFileComp
	}
}

func RayJobNameFunc(clientGetter util.ClientGetter) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		clientset, err := clientGetter.RayClientset()
		if err != nil {
			return []string{}, cobra.ShellCompDirectiveError
		}

		namespace, _, err := clientGetter.ToRawKubeConfigLoader().Namespace()
		if err != nil {
			return []string{}, cobra.ShellCompDirectiveError
		}

		opts := metav1.ListOptions{LabelSelector: constants.ProfileLabel, Limit: completionLimit}
		list, err := clientset.RayV1().RayJobs(namespace).List(cmd.Context(), opts)
		if err != nil {
			return []string{}, cobra.ShellCompDirectiveError
		}

		var validArgs []string
		for _, rayJob := range list.Items {
			if !slices.Contains(args, rayJob.Name) {
				validArgs = append(validArgs, rayJob.Name)
			}
		}

		return validArgs, cobra.ShellCompDirectiveNoFileComp
	}
}
