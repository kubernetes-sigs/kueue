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
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/util"
)

const completionLimit = 100

func NamespaceNameFunc(clientGetter util.ClientGetter) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		clientSet, err := clientGetter.K8sClientSet()
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

func WorkloadNameFunc(clientGetter util.ClientGetter, activeStatus *bool) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		clientSet, err := clientGetter.KueueClientSet()
		if err != nil {
			return []string{}, cobra.ShellCompDirectiveError
		}

		namespace, _, err := clientGetter.ToRawKubeConfigLoader().Namespace()
		if err != nil {
			return []string{}, cobra.ShellCompDirectiveError
		}

		list, err := clientSet.KueueV1beta1().Workloads(namespace).List(cmd.Context(), metav1.ListOptions{Limit: completionLimit})
		if err != nil {
			return []string{}, cobra.ShellCompDirectiveError
		}

		if activeStatus != nil {
			filteredItems := make([]v1beta1.Workload, 0, len(list.Items))
			for _, wl := range list.Items {
				if ptr.Deref(wl.Spec.Active, true) == *activeStatus {
					filteredItems = append(filteredItems, wl)
				}
			}
			list.Items = filteredItems
		}

		var validArgs []string
		for _, wl := range list.Items {
			if !slices.Contains(args, wl.Name) {
				validArgs = append(validArgs, wl.Name)
			}
		}

		return validArgs, cobra.ShellCompDirectiveNoFileComp
	}
}

func ClusterQueueNameFunc(clientGetter util.ClientGetter, activeStatus *bool) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		clientSet, err := clientGetter.KueueClientSet()
		if err != nil {
			return []string{}, cobra.ShellCompDirectiveError
		}

		list, err := clientSet.KueueV1beta1().ClusterQueues().List(cmd.Context(), metav1.ListOptions{Limit: completionLimit})
		if err != nil {
			return []string{}, cobra.ShellCompDirectiveError
		}

		if activeStatus != nil {
			filteredItems := make([]v1beta1.ClusterQueue, 0, len(list.Items))
			for _, cq := range list.Items {
				stopPolicy := ptr.Deref(cq.Spec.StopPolicy, v1beta1.None)
				if *activeStatus && stopPolicy == v1beta1.None {
					filteredItems = append(filteredItems, cq)
				} else if !*activeStatus && stopPolicy != v1beta1.None {
					filteredItems = append(filteredItems, cq)
				}
			}
			list.Items = filteredItems
		}

		validArgs := make([]string, len(list.Items))
		for i, wl := range list.Items {
			validArgs[i] = wl.Name
		}

		return validArgs, cobra.ShellCompDirectiveNoFileComp
	}
}

func LocalQueueNameFunc(clientGetter util.ClientGetter, activeStatus *bool) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		clientSet, err := clientGetter.KueueClientSet()
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

		if activeStatus != nil {
			filteredItems := make([]v1beta1.LocalQueue, 0, len(list.Items))
			for _, lq := range list.Items {
				stopPolicy := ptr.Deref(lq.Spec.StopPolicy, v1beta1.None)
				if *activeStatus && stopPolicy == v1beta1.None {
					filteredItems = append(filteredItems, lq)
				} else if !*activeStatus && stopPolicy != v1beta1.None {
					filteredItems = append(filteredItems, lq)
				}
			}
			list.Items = filteredItems
		}

		validArgs := make([]string, len(list.Items))
		for i, wl := range list.Items {
			validArgs[i] = wl.Name
		}

		return validArgs, cobra.ShellCompDirectiveNoFileComp
	}
}
