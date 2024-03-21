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

package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/importer/pod"
	"sigs.k8s.io/kueue/cmd/importer/util"
	"sigs.k8s.io/kueue/pkg/util/useragent"
)

const (
	NamespaceFlag        = "namespace"
	NamespaceFlagShort   = "n"
	QueueMappingFlag     = "queuemapping"
	QueueMappingFileFlag = "queuemapping-file"
	QueueLabelFlag       = "queuelabel"
	QPSFlag              = "qps"
	BurstFlag            = "burst"
	VerbosityFlag        = "verbose"
	VerboseFlagShort     = "v"
	ConcurrencyFlag      = "concurrent-workers"
	ConcurrencyFlagShort = "c"
	DryRunFlag           = "dry-run"
	AddLabelsFlag        = "add-labels"
)

var (
	rootCmd = &cobra.Command{
		Use:   "importer",
		Short: "Import existing (running) objects into Kueue",
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			v, _ := cmd.Flags().GetCount(VerbosityFlag)
			level := (v + 1) * -1
			ctrl.SetLogger(zap.New(
				zap.UseDevMode(true),
				zap.ConsoleEncoder(),
				zap.Level(zapcore.Level(level)),
			))
			return nil
		},
	}
)

func setFlags(cmd *cobra.Command) {
	cmd.Flags().StringSliceP(NamespaceFlag, NamespaceFlagShort, nil, "target namespaces (at least one should be provided)")
	cmd.Flags().String(QueueLabelFlag, "", "label used to identify the target local queue")
	cmd.Flags().StringToString(QueueMappingFlag, nil, "mapping from \""+QueueLabelFlag+"\" label values to local queue names")
	cmd.Flags().StringToString(AddLabelsFlag, nil, "additional label=value pairs to be added to the imported pods and created workloads")
	cmd.Flags().String(QueueMappingFileFlag, "", "yaml file containing extra mappings from \""+QueueLabelFlag+"\" label values to local queue names")
	cmd.Flags().Float32(QPSFlag, 50, "client QPS, as described in https://kubernetes.io/docs/reference/config-api/apiserver-eventratelimit.v1alpha1/#eventratelimit-admission-k8s-io-v1alpha1-Limit")
	cmd.Flags().Int(BurstFlag, 50, "client Burst, as described in https://kubernetes.io/docs/reference/config-api/apiserver-eventratelimit.v1alpha1/#eventratelimit-admission-k8s-io-v1alpha1-Limit")
	cmd.Flags().UintP(ConcurrencyFlag, ConcurrencyFlagShort, 8, "number of concurrent import workers")
	cmd.Flags().Bool(DryRunFlag, true, "don't import, check the config only")

	_ = cmd.MarkFlagRequired(NamespaceFlag)
	cmd.MarkFlagsRequiredTogether(QueueLabelFlag, QueueMappingFlag)
	cmd.MarkFlagsOneRequired(QueueLabelFlag, QueueMappingFileFlag)
	cmd.MarkFlagsMutuallyExclusive(QueueLabelFlag, QueueMappingFileFlag)
}

func init() {
	rootCmd.AddGroup(&cobra.Group{
		ID:    "pod",
		Title: "Pods import",
	})
	rootCmd.PersistentFlags().CountP(VerbosityFlag, VerboseFlagShort, "verbosity (specify multiple times to increase the log level)")

	importCmd := &cobra.Command{
		Use:     "import",
		GroupID: "pod",
		Short:   "Checks the prerequisites and import pods.",
		RunE:    importCmd,
	}
	setFlags(importCmd)
	rootCmd.AddCommand(importCmd)
}

func main() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func loadMappingCache(ctx context.Context, c client.Client, cmd *cobra.Command) (*util.ImportCache, error) {
	flags := cmd.Flags()
	namespaces, err := flags.GetStringSlice(NamespaceFlag)
	if err != nil {
		return nil, err
	}

	queueLabel, err := flags.GetString(QueueLabelFlag)
	if err != nil {
		return nil, err
	}

	mappingFile, err := flags.GetString(QueueMappingFileFlag)
	if err != nil {
		return nil, err
	}

	var mapping util.MappingRules
	if mappingFile != "" {
		mapping, err = util.MappingRulesFromFile(mappingFile)
		if err != nil {
			return nil, err
		}
	} else {
		queueLabelMapping, err := flags.GetStringToString(QueueMappingFlag)
		if err != nil {
			return nil, err
		}
		mapping = util.MappingRulesForLabel(queueLabel, queueLabelMapping)
	}

	addLabels, err := flags.GetStringToString(AddLabelsFlag)
	if err != nil {
		return nil, err
	}

	var validationErrors []error
	for name, value := range addLabels {
		for _, err := range validation.IsQualifiedName(name) {
			validationErrors = append(validationErrors, fmt.Errorf("name %q: %s", name, err))
		}
		for _, err := range validation.IsValidLabelValue(value) {
			validationErrors = append(validationErrors, fmt.Errorf("label %q value %q: %s", name, value, err))
		}
	}
	if len(validationErrors) > 0 {
		return nil, fmt.Errorf("%s: %w", AddLabelsFlag, errors.Join(validationErrors...))
	}

	return util.LoadImportCache(ctx, c, namespaces, mapping, addLabels)
}

func getKubeClient(cmd *cobra.Command) (client.Client, error) {
	kubeConfig, err := ctrl.GetConfig()
	if err != nil {
		return nil, err
	}
	if kubeConfig.UserAgent == "" {
		kubeConfig.UserAgent = useragent.Default()
	}
	qps, err := cmd.Flags().GetFloat32(QPSFlag)
	if err != nil {
		return nil, err
	}
	kubeConfig.QPS = qps
	bust, err := cmd.Flags().GetInt(BurstFlag)
	if err != nil {
		return nil, err
	}
	kubeConfig.Burst = bust

	if err := kueue.AddToScheme(scheme.Scheme); err != nil {
		return nil, err
	}

	c, err := client.New(kubeConfig, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		return nil, err
	}
	return c, nil
}

func importCmd(cmd *cobra.Command, _ []string) error {
	log := ctrl.Log.WithName("import")
	ctx := ctrl.LoggerInto(context.Background(), log)
	cWorkers, _ := cmd.Flags().GetUint(ConcurrencyFlag)
	c, err := getKubeClient(cmd)
	if err != nil {
		return err
	}

	cache, err := loadMappingCache(ctx, c, cmd)
	if err != nil {
		return err
	}

	if err = pod.Check(ctx, c, cache, cWorkers); err != nil {
		return err
	}

	if dr, _ := cmd.Flags().GetBool(DryRunFlag); dr {
		fmt.Printf("%q is enabled by default, use \"--%s=false\" to continue with the import\n", DryRunFlag, DryRunFlag)
		return nil
	}
	return pod.Import(ctx, c, cache, cWorkers)
}
