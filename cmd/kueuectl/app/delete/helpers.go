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

package delete

import (
	"fmt"

	"github.com/spf13/cobra"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	cascadingFlagName = "cascade"
)

func addCascadingFlag(cmd *cobra.Command) {
	cmd.PersistentFlags().String(
		cascadingFlagName,
		"background",
		`Must be "background", "orphan", or "foreground". Defaults to background.`,
	)
}

func getCascadingStrategy(cmd *cobra.Command) (metav1.DeletionPropagation, error) {
	cascadingFlag, err := cmd.Flags().GetString(cascadingFlagName)
	if err != nil {
		return metav1.DeletePropagationBackground, err
	}
	switch cascadingFlag {
	case "orphan":
		return metav1.DeletePropagationOrphan, nil
	case "foreground":
		return metav1.DeletePropagationForeground, nil
	case "background":
		return metav1.DeletePropagationBackground, nil
	default:
		return metav1.DeletePropagationBackground, fmt.Errorf(`invalid cascade value (%v). Must be "background", "foreground", or "orphan"`, cascadingFlag)
	}
}
