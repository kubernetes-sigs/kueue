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
	goflag "flag"
	"log"
	"os"

	"github.com/spf13/pflag"
	cliflag "k8s.io/component-base/cli/flag"

	"sigs.k8s.io/kueue/cmd/kueuectl-docs/generators"
	"sigs.k8s.io/kueue/cmd/kueuectl/app"
)

func main() {
	var (
		templatesDir string
		outputDir    string
	)

	if len(os.Args) == 3 {
		templatesDir = os.Args[1]
		outputDir = os.Args[2]
	} else {
		log.Fatalf("usage: %s <templates-dir> <output-dir>", os.Args[0])
	}

	cmd := app.NewDefaultKueuectlCmd()
	pflag.CommandLine.SetNormalizeFunc(cliflag.WordSepNormalizeFunc)
	pflag.CommandLine.AddGoFlagSet(goflag.CommandLine)

	if err := generators.GenMarkdownTree(cmd, templatesDir, outputDir); err != nil {
		panic(err)
	}
}
