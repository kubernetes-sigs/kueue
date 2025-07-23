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

package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"syscall"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"sigs.k8s.io/kueue/internal/tools/yaml-processor/yamlproc"
)

type options struct {
	LogLevel       string
	ProcessingPlan string
}

func main() {
	opts, err := parseOptions()
	if err != nil {
		log.Fatalf("Failed to parse options: %v", err)
	}

	logger, err := newLogger(opts.LogLevel)
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	defer func() {
		// Sync logger and handle errors, ignoring specific non-critical errors for console outputs.
		// Ignores syscall.ENOTTY (MacOS: "inappropriate ioctl for device"), syscall.EBADF (MacOS: "bad file descriptor")
		// and syscall.EINVAL (Linux: "invalid argument") when syncing to non-file descriptors like
		// /dev/stderr or /dev/stdout.
		if err := logger.Sync(); err != nil && !errors.Is(err, syscall.ENOTTY) && !errors.Is(err, syscall.EBADF) && !errors.Is(err, syscall.EINVAL) {
			log.Fatalf("Error syncing logger: %v", err)
		}
	}()
	yamlproc.SetLogger(logger)

	processingPlan, err := yamlproc.LoadProcessingPlan(opts.ProcessingPlan)
	if err != nil {
		logger.Fatal("Failed to load processing plan", zap.Error(err))
	}

	yqClient := yamlproc.NewYQClient()
	textInserter := yamlproc.NewTextInserter(yqClient)
	fileProcessor := yamlproc.NewProcessor(yqClient, textInserter)

	fileProcessor.ProcessPlan(*processingPlan)
}

func parseOptions() (*options, error) {
	opts := &options{}

	flag.StringVar(&opts.LogLevel, "zap-log-level", "info", "Minimum enabled logging level")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: yaml-processor <processing-plan.yaml>\n")
		flag.PrintDefaults()
	}

	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		return nil, errors.New("exactly one processing plan file argument is required")
	}

	opts.ProcessingPlan = flag.Arg(0)

	return opts, nil
}

func newLogger(logLevel string) (*zap.Logger, error) {
	atomicLevel, err := zap.ParseAtomicLevel(logLevel)
	if err != nil {
		return nil, err
	}

	loggerConfig := zap.NewProductionConfig()
	loggerConfig.Level = atomicLevel
	loggerConfig.Encoding = "console"
	loggerConfig.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	return loggerConfig.Build()
}
