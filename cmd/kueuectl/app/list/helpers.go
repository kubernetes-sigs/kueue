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
	"errors"
	"os"
	"strconv"

	"github.com/spf13/cobra"
)

const (
	defaultListRequestLimit         = 100
	KueuectlListRequestLimitEnvName = "KUEUECTL_LIST_REQUEST_LIMIT"
)

var (
	invalidListRequestLimitError = errors.New("invalid list request limit")
)

func listRequestLimit() (int64, error) {
	listRequestLimitEnv := os.Getenv(KueuectlListRequestLimitEnvName)

	if len(listRequestLimitEnv) == 0 {
		return defaultListRequestLimit, nil
	}

	limit, err := strconv.ParseInt(listRequestLimitEnv, 10, 64)
	if err != nil {
		return 0, invalidListRequestLimitError
	}

	return limit, nil
}

func addFieldSelectorFlagVar(cmd *cobra.Command, p *string) {
	cmd.Flags().StringVar(p, "field-selector", "",
		"Selector (field query) to filter on, supports '=', '==', and '!='.(e.g. --field-selector key1=value1,key2=value2). The server only supports a limited number of field queries per type.")
}

func addLabelSelectorFlagVar(cmd *cobra.Command, p *string) {
	cmd.Flags().StringVarP(p, "selector", "l", "",
		"Selector (label query) to filter on, supports '=', '==', and '!='.(e.g. -l key1=value1,key2=value2). Matching objects must satisfy all of the specified label constraints.")
}

func addAllNamespacesFlagVar(cmd *cobra.Command, p *bool) {
	cmd.Flags().BoolVarP(p, "all-namespaces", "A", false,
		"If present, list the requested object(s) across all namespaces. Namespace in current context is ignored even if specified with --namespace.")
}

func addClusterQueueFilterFlagVar(cmd *cobra.Command, p *string) {
	cmd.Flags().StringVarP(p, "clusterqueue", "c", "",
		"Filter by cluster queue name which is associated with the resource.")
}

func addLocalQueueFilterFlagVar(cmd *cobra.Command, p *string) {
	cmd.Flags().StringVarP(p, "localqueue", "q", "",
		"Filter by local queue name which is associated with the resource.")
}

func addActiveFilterFlagVar(cmd *cobra.Command, p *[]bool) {
	cmd.Flags().BoolSliceVar(p, "active", make([]bool, 0),
		"Filter by active status. Valid values: 'true' and 'false'.")
}
