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
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	zaplog "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/yaml"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/performance/scheduler/runner/controller"
	"sigs.k8s.io/kueue/test/performance/scheduler/runner/generator"
	"sigs.k8s.io/kueue/test/performance/scheduler/runner/recorder"
	"sigs.k8s.io/kueue/test/performance/scheduler/runner/scraper"
	"sigs.k8s.io/kueue/test/performance/scheduler/runner/stats"
)

var (
	outputDir       = flag.String("o", "", "output directory")
	crdsPath        = flag.String("crds", "", "crds path")
	generatorConfig = flag.String("generatorConfig", "", "generator config file")
	timeout         = flag.Duration("timeout", 10*time.Minute, "maximum record time")
	qps             = flag.Float64("qps", 0, "qps used by the runner clients, use default if 0")
	burst           = flag.Int("burst", 0, "qps used by the runner clients, use default if 0")

	// metrics scarping
	metricsScrapeInterval = flag.Duration("metricsScrapeInterval", 0, "the duration between two metrics scraping, if 0 the metrics scraping is disabled")
	metricsScrapeURL      = flag.String("metricsScrapeURL", "", "the URL to scrape metrics from, ignored when minimal kueue is used")

	// related to minimalkueue
	minimalKueuePath = flag.String("minimalKueue", "", "path to minimalkueue, run in the hosts default cluster if empty")
	withCPUProfile   = flag.Bool("withCPUProfile", false, "generate a CPU profile for minimalkueue")
	withLogs         = flag.Bool("withLogs", false, "capture minimalkueue logs")
	logLevel         = flag.Int("withLogsLevel", 2, "set minimalkueue logs level")
	logToFile        = flag.Bool("logToFile", false, "capture minimalkueue logs to files")
)

var (
	scheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kueue.AddToScheme(scheme))
	utilruntime.Must(kueuealpha.AddToScheme(scheme))
	utilruntime.Must(configapi.AddToScheme(scheme))
}

func main() {
	opts := zap.Options{
		TimeEncoder: zapcore.RFC3339NanoTimeEncoder,
		ZapOpts:     []zaplog.Option{zaplog.AddCaller()},
		Development: true,
		Level:       zaplog.NewAtomicLevelAt(-2),
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	log := zap.New(zap.UseFlagOptions(&opts))

	ctrl.SetLogger(log)

	log.Info("Start runner", "outputDir", outputDir, "crdsPath", crdsPath)
	errCh := make(chan error, 3)
	wg := &sync.WaitGroup{}
	ctx, ctxCancel := context.WithCancel(ctrl.LoggerInto(context.Background(), log))
	var cfg *rest.Config

	if *minimalKueuePath != "" {
		testEnv := &envtest.Environment{
			CRDDirectoryPaths:     []string{*crdsPath},
			ErrorIfCRDPathMissing: true,
		}

		var err error
		cfg, err = testEnv.Start()
		if err != nil {
			log.Error(err, "Starting test env")
			os.Exit(1)
		}
		defer func() {
			err := testEnv.Stop()
			if err != nil {
				log.Error(err, "Stopping test env")
			}
		}()

		// prepare the kubeconfig
		kubeconfig, err := utiltesting.RestConfigToKubeConfig(cfg)
		if err != nil {
			log.Error(err, "Generate kubeconfig")
			os.Exit(1)
		}

		kubeconfigPath := filepath.Join(*outputDir, "kubeconfig")
		err = os.WriteFile(kubeconfigPath, kubeconfig, 00660)
		if err != nil {
			log.Error(err, "Write kubeconfig")
			os.Exit(1)
		}

		metricsPort := 0
		if *metricsScrapeInterval != 0 {
			metricsPort, err = scraper.GetFreePort()
			if err != nil {
				log.Error(err, "getting a free port, metrics scraping disabled")
			}
			metricsScrapeURL = ptr.To(fmt.Sprintf("http://localhost:%d/metrics", metricsPort))
		}

		// start the minimal kueue manager process
		err = runCommand(ctx, *outputDir, *minimalKueuePath, "kubeconfig", *withCPUProfile, *withLogs, *logToFile, *logLevel, errCh, wg, metricsPort)
		if err != nil {
			log.Error(err, "MinimalKueue start")
			os.Exit(1)
		}
	} else {
		var err error
		cfg, err = ctrl.GetConfig()
		if err != nil {
			log.Error(err, "Get config")
			os.Exit(1)
		}
	}

	if *qps > 0 {
		cfg.QPS = float32(*qps)
	}

	if *burst > 0 {
		cfg.Burst = *burst
	}

	generationDoneCh := make(chan struct{})
	err := runGenerator(ctx, cfg, *generatorConfig, errCh, wg, generationDoneCh)
	if err != nil {
		log.Error(err, "Generator start")
		os.Exit(1)
	}

	recorder, err := startRecorder(ctx, errCh, wg, generationDoneCh, *timeout)
	if err != nil {
		log.Error(err, "Recorder start")
		os.Exit(1)
	}

	if *metricsScrapeInterval != 0 && *metricsScrapeURL != "" {
		dumpTar := filepath.Join(*outputDir, "metricsDump.tgz")
		err := runScraper(ctx, *metricsScrapeInterval, dumpTar, *metricsScrapeURL, errCh, wg)
		if err != nil {
			log.Error(err, "Scraper start")
			os.Exit(1)
		}
	}

	err = runManager(ctx, cfg, errCh, wg, recorder)
	if err != nil {
		log.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)
	endWithError := false
	select {
	case <-done:
		log.Info("Done")
	case err := <-errCh:
		if err != nil {
			log.Error(err, "Error")
			endWithError = true
		}
	}
	ctxCancel()
	wg.Wait()

	if endWithError {
		os.Exit(1)
	}

	err = recorder.WriteSummary(filepath.Join(*outputDir, "summary.yaml"))
	if err != nil {
		log.Error(err, "Writing summary")
		os.Exit(1)
	}
	err = recorder.WriteCQCsv(filepath.Join(*outputDir, "cqStates.csv"))
	if err != nil {
		log.Error(err, "Writing cq csv")
		os.Exit(1)
	}
	err = recorder.WriteWLCsv(filepath.Join(*outputDir, "wlStates.csv"))
	if err != nil {
		log.Error(err, "Writing wl csv")
		os.Exit(1)
	}

	if *minimalKueuePath == "" {
		c, err := client.New(cfg, client.Options{Scheme: scheme})
		if err != nil {
			log.Error(err, "Create cleanup client")
		} else {
			generator.Cleanup(context.Background(), c)
		}
	}
}

func runCommand(ctx context.Context, workDir, cmdPath, kubeconfig string, withCPUProf, withLogs, logToFile bool, logLevel int, errCh chan<- error, wg *sync.WaitGroup, metricsPort int) error {
	log := ctrl.LoggerFrom(ctx).WithName("Run command")

	cmd := exec.CommandContext(ctx, cmdPath, "--kubeconfig", filepath.Join(workDir, kubeconfig))
	cmd.Cancel = func() error {
		log.Info("Stop the command")
		return cmd.Process.Signal(syscall.SIGINT)
	}

	exe := path.Base(cmdPath)

	if withCPUProf {
		cmd.Args = append(cmd.Args, "--cpuprofile", filepath.Join(workDir, fmt.Sprintf("%s.cpu.prof", exe)))
	}

	if withLogs {
		cmd.Args = append(cmd.Args, fmt.Sprintf("--zap-log-level=%d", logLevel))
		outWriter := os.Stdout
		errWriter := os.Stderr
		if logToFile {
			var err error
			outWriter, err = os.Create(filepath.Join(workDir, fmt.Sprintf("%s.out.log", exe)))
			if err != nil {
				return err
			}
			defer outWriter.Close()

			errWriter, err = os.Create(filepath.Join(workDir, fmt.Sprintf("%s.err.log", exe)))
			if err != nil {
				return err
			}
			defer errWriter.Close()
		}
		cmd.Stdout = outWriter
		cmd.Stderr = errWriter
	}

	if metricsPort != 0 {
		cmd.Args = append(cmd.Args, "--metricsPort", strconv.Itoa(metricsPort))
	}

	log.Info("Starting process", "path", cmd.Path, "args", cmd.Args)
	err := cmd.Start()
	if err != nil {
		return err
	}
	startTime := time.Now()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := cmd.Wait()
		if err != nil {
			select {
			case <-ctx.Done():
				// nothing to do here
			default:
				errCh <- fmt.Errorf("command: %s", err)
			}
		}

		cs := stats.CmdStats{
			WallMs: time.Since(startTime).Milliseconds(),
			UserMs: cmd.ProcessState.UserTime().Milliseconds(),
			SysMs:  cmd.ProcessState.SystemTime().Milliseconds(),
			Maxrss: -1,
		}
		if rusage, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
			cs.Maxrss = rusage.Maxrss
		}
		log.Info("Cmd ended", "stats", cs)
		csBytes, err := yaml.Marshal(cs)
		if err != nil {
			log.Error(err, "Marshaling cmd stats")
			return
		}

		err = os.WriteFile(filepath.Join(workDir, fmt.Sprintf("%s.stats.yaml", exe)), csBytes, 0666)
		if err != nil {
			log.Error(err, "Writing cmd stats")
		}
	}()
	return nil
}

func runGenerator(ctx context.Context, cfg *rest.Config, generatorConfig string, errCh chan<- error, wg *sync.WaitGroup, genDone chan<- struct{}) error {
	log := ctrl.LoggerFrom(ctx).WithName("Run generator")
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "Create generator's client")
		close(genDone)
		return err
	}

	cohorts, err := generator.LoadConfig(generatorConfig)
	if err != nil {
		log.Error(err, "Loading config")
		close(genDone)
		return err
	}

	statTime := time.Now()
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(genDone)
		err := generator.Generate(ctx, c, cohorts)
		if err != nil {
			log.Error(err, "generating")
			errCh <- err
			return
		}
		log.Info("Generator done", "duration", time.Since(statTime))
	}()

	log.Info("Generator started", "qps", cfg.QPS, "burst", cfg.Burst)
	return nil
}

func startRecorder(ctx context.Context, errCh chan<- error, wg *sync.WaitGroup, genDone <-chan struct{}, recordTimeout time.Duration) (*recorder.Recorder, error) {
	log := ctrl.LoggerFrom(ctx).WithName("Start recorder")
	recorder := recorder.New(recordTimeout)
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := recorder.Run(ctx, genDone)
		if err != nil {
			log.Error(err, "Recorder run")
		} else {
			log.Info("Recorder done")
		}
		errCh <- err
	}()

	log.Info("Recorder started", "timeout", recordTimeout)
	return recorder, nil
}

func runManager(ctx context.Context, cfg *rest.Config, errCh chan<- error, wg *sync.WaitGroup, r *recorder.Recorder) error {
	log := ctrl.LoggerFrom(ctx).WithName("Run manager")

	options := ctrl.Options{
		Scheme: scheme,
		Controller: crconfig.Controller{
			SkipNameValidation: ptr.To(true),
			GroupKindConcurrency: map[string]int{
				kueue.GroupVersion.WithKind("Workload").GroupKind().String(): 5,
			},
		},
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
	}
	mgr, err := ctrl.NewManager(cfg, options)
	if err != nil {
		log.Error(err, "Creating manager")
		return err
	}

	if err := controller.NewReconciler(mgr.GetClient(), r).SetupWithManager(mgr); err != nil {
		log.Error(err, "Setup controller")
		return err
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Info("Starting manager")
		if err := mgr.Start(ctx); err != nil {
			log.Error(err, "Could not run manager")
			errCh <- err
		} else {
			log.Info("End manager")
		}
	}()

	log.Info("Manager started")
	return nil
}

func runScraper(ctx context.Context, interval time.Duration, output, url string, errCh chan<- error, wg *sync.WaitGroup) error {
	log := ctrl.LoggerFrom(ctx).WithName("Run metrics scraper")

	s := scraper.NewScraper(interval, url, "%d.prometheus")

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := s.Run(ctx, output)
		if err != nil {
			log.Error(err, "Running the scraper")
			errCh <- err
			return
		}
		log.Info("Scrape done")
	}()

	log.Info("Scrape started")
	return nil
}
