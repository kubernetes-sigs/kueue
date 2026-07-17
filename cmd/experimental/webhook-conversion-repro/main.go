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

// Creates N Kueue Workloads stored as v1beta1 directly via the API server.
// Runs either as an in-cluster Job (using rest.InClusterConfig) or locally
// from the host machine (using ~/.kube/config).
//
// Each workload is padded with multiple containers and massive environment variables
// to intentionally bloat the JSON payload size. This heavy payload stresses the
// API Server's webhook conversion path during cold restarts, which is the primary
// lever for reproducing the etcd compaction instability.
package main

import (
	"context"
	crypto_rand "crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type Config struct {
	Count      int
	Start      int
	QPS        float64
	Workers    int
	Containers int
	Envs       int
	Kubeconfig string
}

func setupFlags(cfg *Config) {
	flag.IntVar(&cfg.Count, "count", -1, "Number of workloads to create (required)")
	flag.IntVar(&cfg.Start, "start", 0, "First workload index (wl-<start>)")
	flag.Float64Var(&cfg.QPS, "qps", 50.0, "API server QPS limit")
	flag.IntVar(&cfg.Workers, "workers", -1, "Concurrent create workers (required)")
	flag.IntVar(&cfg.Containers, "containers", -1, "Containers per workload (required)")
	flag.IntVar(&cfg.Envs, "envs", -1, "Env vars per container (required)")
	flag.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
}

func validateFlags(cfg *Config) error {
	if cfg.Count <= 0 || cfg.QPS <= 0 || cfg.Workers <= 0 || cfg.Containers <= 0 || cfg.Envs < 0 {
		return errors.New("--count, --qps, --workers, and --containers must be strictly positive (> 0). --envs must be >= 0")
	}
	return nil
}

func getKubeClient(kubeconfig string, qps float64) (dynamic.Interface, error) {
	var config *rest.Config
	if kubeconfig == "" {
		if home := os.Getenv("HOME"); home != "" {
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
	}

	var configErr error
	if kubeconfig != "" {
		config, configErr = clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	if configErr != nil || config == nil {
		var inClusterErr error
		config, inClusterErr = rest.InClusterConfig()
		if inClusterErr != nil {
			if configErr != nil {
				return nil, fmt.Errorf("failed to load kubeconfig (out-of-cluster: %v, in-cluster: %v)", configErr, inClusterErr)
			}
			return nil, fmt.Errorf("failed to load in-cluster config: %w", inClusterErr)
		}
	}
	config.QPS = float32(qps)
	config.Burst = int(qps) * 2

	return dynamic.NewForConfig(config)
}

func generatePadString(length int) string {
	if length <= 0 {
		return ""
	}
	b := make([]byte, (length+1)/2)
	if _, err := crypto_rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)[:length]
}

func createWorkloadObj(idx, containers, envs int) *unstructured.Unstructured {
	cList := make([]any, 0, containers)
	for c := range containers {
		env := make([]any, 0, envs)
		for e := range envs {
			env = append(env, map[string]any{
				"name":  fmt.Sprintf("K%d_%d", c, e),
				"value": generatePadString(100),
			})
		}
		cList = append(cList, map[string]any{
			"name":    fmt.Sprintf("c%d", c),
			"image":   "x",
			"command": []any{"s"},
			"env":     env,
			"resources": map[string]any{
				"requests": map[string]any{"cpu": "1m", "memory": "1Mi"},
			},
		})
	}

	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kueue.x-k8s.io/v1beta1",
		"kind":       "Workload",
		"metadata":   map[string]any{"name": fmt.Sprintf("wl-%d", idx), "namespace": "default"},
		"spec": map[string]any{
			"queueName": "local-queue",
			"podSets": []any{map[string]any{
				"count": int64(1), "name": "main",
				"template": map[string]any{
					"spec": map[string]any{"containers": cList},
				},
			}},
		},
	}}
}

func main() {
	var cfg Config
	setupFlags(&cfg)
	flag.Parse()

	if err := validateFlags(&cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		flag.Usage()
		os.Exit(1)
	}

	client, err := getKubeClient(cfg.Kubeconfig, cfg.QPS)
	if err != nil {
		log.Fatalf("Failed to create kubernetes client: %v", err)
	}

	gvr := schema.GroupVersionResource{Group: "kueue.x-k8s.io", Version: "v1beta1", Resource: "workloads"}
	ctx := context.Background()
	var wg sync.WaitGroup
	jobs := make(chan int, cfg.Workers)
	var success, failed int32

	fmt.Printf("Creating %d workloads (start=%d) at %.0f QPS with %d workers...\n", cfg.Count, cfg.Start, cfg.QPS, cfg.Workers)
	startTime := time.Now()

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s := atomic.LoadInt32(&success)
				f := atomic.LoadInt32(&failed)
				fmt.Printf("\r\033[KProgress: %d / %d (Failed: %d)", s, cfg.Count, f)
			case <-done:
				return
			}
		}
	}()

	for range cfg.Workers {
		wg.Go(func() {
			for idx := range jobs {
				wl := createWorkloadObj(idx, cfg.Containers, cfg.Envs)
				var lastErr error
				created := false
				for range 5 {
					reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
					_, err := client.Resource(gvr).Namespace("default").Create(reqCtx, wl, metav1.CreateOptions{})
					if err == nil || apierrors.IsAlreadyExists(err) {
						cancel()
						atomic.AddInt32(&success, 1)
						created = true
						break
					} else {
						lastErr = err
					}
					cancel()
					time.Sleep(2 * time.Second)
				}
				if !created {
					fmt.Printf("\nWorkload creation failed: %v\n", lastErr)
					atomic.AddInt32(&failed, 1)
					log.Fatalf("Quick abort: webhook crashed during population")
				}
			}
		})
	}

	for i := cfg.Start; i < cfg.Start+cfg.Count; i++ {
		jobs <- i
	}
	close(jobs)

	wg.Wait()
	close(done)
	fmt.Printf("\nDone in %v. Success: %d  Failed: %d\n", time.Since(startTime), success, failed)
	if failed > 0 {
		log.Fatalf("%d workloads failed to create", failed)
	}
}
