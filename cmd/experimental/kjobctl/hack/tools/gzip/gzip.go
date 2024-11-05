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
	"compress/gzip"
	"io"
	"os"
)

// Simple gzip encoder used to have the same output generated regardless of the OS.
func main() {
	writer, err := gzip.NewWriterLevel(os.Stdout, gzip.BestCompression)
	if err != nil {
		panic(err)
	}
	// The header is initialized with Unknown OS and no additional fields, we should not change it.
	_, err = io.Copy(writer, os.Stdin)
	if err != nil {
		panic(err)
	}
	writer.Close()
}
