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
	"log"
	"os"
	"syscall"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"sigs.k8s.io/kueue/internal/tools/yaml-processor/yamlproc"
)

func main() {
	logger, err := newLogger()
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

	if len(os.Args) < 2 {
		logger.Fatal("Usage: ./yaml-processor <processing-plan.yaml>")
	}

	planPath := os.Args[1]
	processingPlan, err := yamlproc.LoadProcessingPlan(planPath)
	if err != nil {
		logger.Fatal("Failed to load processing plan", zap.Error(err))
	}

	yqClient := yamlproc.NewYQClient()
	textInserter := yamlproc.NewTextInserter(yqClient)
	fileProcessor := yamlproc.NewProcessor(yqClient, textInserter)

	fileProcessor.ProcessPlan(*processingPlan)
}

func newLogger() (*zap.Logger, error) {
	loggerConfig := zap.NewProductionConfig()
	loggerConfig.Encoding = "console"
	loggerConfig.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	return loggerConfig.Build()
}
