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
	"bytes"
	"flag"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

type config struct {
	// From env variables
	JobCompletionIndex int32  // completion index of the job.
	PodIP              string // current pod IP

	// From flags
	InputEnvFile       string
	OutputPath         string
	Indexes            string
	FirstNodeIP        bool
	FirstNodeIPTimeout time.Duration

	// From input env
	Variables map[string]string
}

func (c *config) load() error {
	if err := c.loadFromEnv(); err != nil {
		return err
	}

	if err := c.loadFromFlags(); err != nil {
		return err
	}

	if err := c.loadEnvFile(); err != nil {
		return err
	}

	return nil
}

func (c *config) loadFromEnv() error {
	jobCompletionIndex := os.Getenv("JOB_COMPLETION_INDEX")
	jobCompletionIndexInt, err := strconv.ParseInt(jobCompletionIndex, 10, 32)
	if err != nil {
		return fmt.Errorf("error on parse `JOB_COMPLETION_INDEX`: %w", err)
	}

	c.JobCompletionIndex = int32(jobCompletionIndexInt)
	c.PodIP = os.Getenv("POD_IP")

	return nil
}

func (c *config) loadFromFlags() error {
	flag.StringVar(&c.InputEnvFile, "input", "", "Input Slurm env variables path.")
	flag.StringVar(&c.OutputPath, "output", "", "Output generated variables file path.")
	flag.StringVar(&c.OutputPath, "indexes", "", "The indexes specification identifies what array index values should be used.")
	flag.BoolVar(&c.FirstNodeIP, "first-node-ip", false, "Enable the retrieval of the first node's IP address.")
	flag.DurationVar(&c.FirstNodeIPTimeout, "first-node-ip-timeout", time.Minute, "The timeout for the retrieval of the first node's IP address.")

	flag.Parse()

	if flag.Parsed() {
		return nil
	}

	return nil
}

func (c *config) loadEnvFile() error {
	data, err := os.ReadFile(c.InputEnvFile)
	if err != nil {
		return fmt.Errorf("error on reading env file: %w", err)
	}

	c.Variables = parseEnvFile(data)

	return nil
}

func parseEnvFile(data []byte) map[string]string {
	parts := bytes.Split(data, []byte{'\n'})
	gotOut := make(map[string]string, len(parts))
	for _, part := range parts {
		pair := bytes.Split(part, []byte{'='})
		if len(pair) == 2 {
			gotOut[string(pair[0])] = string(pair[1])
		}
	}
	return gotOut
}

func main() {
	cfg := &config{}

	if err := cfg.load(); err != nil {
		panic(err)
	}

	if err := process(cfg); err != nil {
		panic(err)
	}
}

func process(cfg *config) error {
	completionsIndexes := strings.Split(cfg.Indexes, ";")
	if cfg.JobCompletionIndex >= int32(len(completionsIndexes)) {
		return nil
	}

	env := make(map[string]string, 1)

	slurmNTasksPerNode, err := strconv.ParseInt(cfg.Variables["JOB_COMPLETION_INDEX"], 10, 32)
	if err != nil {
		return fmt.Errorf("error on parse `JOB_COMPLETION_INDEX`: %w", err)
	}

	slurmArrayJobID, err := strconv.ParseInt(cfg.Variables["SLURM_ARRAY_JOB_ID"], 10, 32)
	if err != nil {
		return fmt.Errorf("error on parse `SLURM_ARRAY_JOB_ID`: %w", err)
	}

	containersIndexes := strings.Split(completionsIndexes[cfg.JobCompletionIndex], ",")
	for index, containerIndex := range containersIndexes {
		if err := os.MkdirAll(path.Join(cfg.OutputPath, strconv.Itoa(index)), 0755); err != nil {
			return fmt.Errorf("error on creating output directory: %w", err)
		}

		slurmJobID := cfg.JobCompletionIndex*int32(slurmNTasksPerNode) + int32(index) + int32(slurmArrayJobID)
		env["SLURM_JOB_ID"] = fmt.Sprint(slurmJobID)
		env["SLURM_JOBID"] = env["SLURM_JOB_ID"]
		env["SLURM_ARRAY_TASK_ID"] = containerIndex
	}

	return nil
}
