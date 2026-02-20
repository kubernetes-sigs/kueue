---
title: "Running and debugging tests"
linkTitle: "Running tests"
weight: 25
description: >
  Running and debugging tests
---

## Running presubmission verification tests
```shell
make verify
```

## Running unit tests
To run all unit tests:
```shell
make test
```

To run unit tests for webhooks:
```shell
go test ./pkg/webhooks
```
To run tests that match `TestValidateClusterQueue` regular expression [ref](https://pkg.go.dev/cmd/go#hdr-Testing_flags):
```shell
go test ./pkg/webhooks -run TestValidateClusterQueue
```

### Running unit tests with race detection

Use `-race` to enable Go's built-in race detector:
```shell
go test ./pkg/scheduler/preemption/ -race
```

### Running unit tests with stress

To run unit tests in a loop and collect failures:
```shell
# install go stress
go install golang.org/x/tools/cmd/stress@latest
# compile tests (you can add -race for race detection)
go test ./pkg/scheduler/preemption/ -c
# it loops and reports failures
stress ./preemption.test -test.run TestPreemption
```

## Running integration tests

```shell
make test-integration
```

For running a subset of tests, see [Running subset of tests](#running-subset-of-integration-or-e2e-tests).

## Running e2e tests using custom build
```shell
make kind-image-build
make test-e2e
make test-tas-e2e
make test-e2e-customconfigs
make test-e2e-certmanager
make test-e2e-kueueviz
make test-multikueue-e2e
```

You can specify the Kubernetes version used for running the e2e tests by setting the `E2E_K8S_FULL_VERSION` variable:
```shell
E2E_K8S_FULL_VERSION=1.35.0 make test-e2e
```

For running a subset of tests, see [Running subset of tests](#running-subset-of-integration-or-e2e-tests).

## Increase logging verbosity
`TEST_LOG_LEVEL` controls test logging uniformly for all targets:

- `go test`, `make test` (unit tests)
- `make test-integration` (integration tests)
- `make test-*-e2e` (e2e tests)

Use more negative values for more verbose logs and higher (positive) values for quieter logs. For example:
```shell
TEST_LOG_LEVEL=-5 make test-integration   # more verbose
TEST_LOG_LEVEL=-1 make test               # less verbose than default
```
Default is `TEST_LOG_LEVEL=-3`.

## Debug tests in VSCode
It is possible to debug unit and integration tests in VSCode.
You need to have the [Go extension](https://marketplace.visualstudio.com/items?itemName=golang.Go) installed.
Now you will have `run test | debug test` text buttons above lines like
```go
func TestValidateClusterQueue(t *testing.T) {
```
You can click on the `debug test` to debug a specific test.

For integration tests, an additional step is needed.  In settings.json, you need to add two variables inside `go.testEnvVars`:
- Run `ENVTEST_K8S_VERSION=1.35 make envtest && ./bin/setup-envtest use $ENVTEST_K8S_VERSION -p path` and assign the path to the `KUBEBUILDER_ASSETS` variable
- Set `KUEUE_BIN` to the `bin` directory within your cloned Kueue repository
```json
"go.testEnvVars": {
    "KUBEBUILDER_ASSETS": "<path from output above>",
    "KUEUE_BIN": "<path-to-your-kueue-folder>/bin",
  },
```

For e2e tests, you can also use [Ginkgo Test Explorer](https://marketplace.visualstudio.com/items?itemName=joselitofilho.ginkgotestexplorer).  You need to add the following variables to settings.json:
```json
 "ginkgotestexplorer.testEnvVars": {
        "KIND_CLUSTER_NAME": "kind",
        "WORKER1_KIND_CLUSTER_NAME": "kind-worker1",
        "MANAGER_KIND_CLUSTER_NAME": "kind-manager",
        "WORKER2_KIND_CLUSTER_NAME": "kind-worker2",
        "KIND": "<your_kueue_path>/bin/kind",
    },
```
and then you can use GUI of the Ginkgo Test Explorer to run individual tests, provided you started kind clanter (see [here](#attaching-e2e-tests-to-an-existing-kind-cluster) for the instructions).

## Attaching e2e tests to an existing kind cluster
You can use the following approach to start up a kind cluster and then run e2e tests from commandline or VSCode,
attaching them to the existing cluster. For example, suppose you want to test some of the multikueue-e2e tests.

### DEV mode (recommended)
Use `E2E_MODE=dev` to create-or-reuse a kind cluster, rebuild/redeploy Kueue, run tests, and keep the cluster running for fast reruns and post-test investigation:

```shell
# Create if missing, otherwise reuse cluster. Rebuild image, run tests, keep the cluster.
E2E_MODE=dev make kind-image-build test-e2e

# MultiKueue dev mode
E2E_MODE=dev make kind-image-build test-multikueue-e2e

# Loop a suite (until it fails) while keeping the cluster
E2E_MODE=dev GINKGO_ARGS="--until-it-fails" make kind-image-build  test-e2e
```

{{% alert title="Note" color="primary" %}}
When reusing a kept cluster in `E2E_MODE=dev`, external operators (MPI, KubeRay, etc.) are installed only once.
To force re-installing them on every run, set `E2E_ENFORCE_OPERATOR_UPDATE=true`.
{{% /alert %}}

To delete the kept cluster(s) afterwards:
- For regular e2e tests, run:
    ```shell
    kind delete clusters kind
    ```
- For MultiKueue tests, run:
    ```shell
    kind delete clusters kind kind-manager kind-worker1 kind-worker2
    ```

### Legacy: interactive attach mode
Run `E2E_RUN_ONLY_ENV=true make kind-image-build test-multikueue-e2e` and wait for the `Do you want to cleanup? [Y/n] ` to appear (CI-style behavior).

The cluster is ready, and now you can run tests from another terminal:
```shell
<your_kueue_path>/bin/ginkgo --json-report ./ginkgo.report -focus "MultiKueue when Creating a multikueue admission check Should run a jobSet on worker if admitted" -r
```
or from VSCode.

## Debugging metrics with Prometheus

To provision a Kind cluster with Prometheus pre-configured for metrics debugging:

```shell
E2E_MODE=dev GINKGO_ARGS="--label-filter=feature:prometheus" make kind-image-build test-e2e
```

For more details, see [Setup Dev Monitoring](/docs/tasks/dev/setup_dev_monitoring).

## Running subset of integration or e2e tests

### Use label filters for integration tests
Integration tests are labeled by controller, job type, feature, and area to enable targeted test execution. You can use `INTEGRATION_FILTERS` with `--label-filter` to run specific test subsets:

**Label Taxonomy:**
- Controllers: `controller:workload`, `controller:localqueue`, `controller:clusterqueue`, `controller:admissioncheck`, `controller:resourceflavor`, `controller:provisioning`
- Job Types: `job:batch`, `job:pod`, `job:jobset`, `job:pytorch`, `job:tensorflow`, `job:mpi`, `job:paddle`, `job:xgboost`, `job:jax`, `job:train`, `job:ray`, `job:appwrapper`
- Features: `feature:tas`, `feature:multikueue`, `feature:provisioning`, `feature:fairsharing`, `feature:admissionfairsharing`
- Areas: `area:core`, `area:jobs`, `area:admissionchecks`, `area:multikueue`

**Examples:**
```shell
# Run only LocalQueue tests
INTEGRATION_FILTERS="--label-filter=controller:localqueue" make test-integration

# Run all job tests
INTEGRATION_FILTERS="--label-filter=area:jobs" make test-integration

# Run PyTorch job tests
INTEGRATION_FILTERS="--label-filter=job:pytorch" make test-integration

# Run all tests except slow
INTEGRATION_FILTERS="--label-filter=!slow" make test-integration

# Run core tests except slow
INTEGRATION_FILTERS="--label-filter=area:core && !slow" make test-integration

# Run TAS-related tests
INTEGRATION_FILTERS="--label-filter=feature:tas" make test-integration

# Run FairSharing tests
INTEGRATION_FILTERS="--label-filter=feature:fairsharing" make test-integration
```

### Use label filters for e2e singlecluster tests
SingleCluster tests are labeled by feature and area. You can use `GINKGO_ARGS` with `--label-filter` to run specific tests:

**Label Taxonomy:**
- Features: `appwrapper,certs,deployment,job,fairsharing,jaxjob,jobset,kuberay,kueuectl,leaderworkerset,metrics,pod,pytorchjob,statefulset,tas,trainjob,visibility,e2e_v1beta1`

**Examples:**
```shell
# Run only appwrapper tests
GINKGO_ARGS="--label-filter=feature:appwrapper" make test-e2e

# Run only deployment tests with helm
GINKGO_ARGS="--label-filter=feature:deployment" make test-e2e-helm

# Run only jobset and trainjob tests with helm
GINKGO_ARGS="--label-filter=feature:jobset,feature:trainjob" make test-e2e
```

### Use Ginkgo --focus arg
```shell
GINKGO_ARGS="--focus=Scheduler" make test-integration
GINKGO_ARGS="--focus='Creating a Pod requesting TAS'" make test-e2e
```
### Use ginkgo.FIt
If you want to focus on specific tests, you can change
`ginkgo.It` to `ginkgo.FIt` for these tests.
For more details, see [here](https://onsi.github.io/ginkgo/#focused-specs).
Then the other tests will be skipped.
For example, you can change
```go
ginkgo.It("Should place pods based on the ranks-ordering", func() {
```
to
```go
ginkgo.FIt("Should place pods based on the ranks-ordering", func() {
```
and then run
```shell
# build and pull image
make test-tas-e2e
```
to test a particular TAS e2e test.

### Use INTEGRATION_TARGET
```shell
INTEGRATION_TARGET='test/integration/singlecluster/scheduler' make test-integration
```

## Flaky integration/e2e tests
You can use --until-it-fails or --repeat=N arguments to Ginkgo to run tests repeatedly, such as:
```shell
GINKGO_ARGS="--until-it-fails" make test-integration
GINKGO_ARGS="--repeat=10" make test-e2e
```
See more [here](https://onsi.github.io/ginkgo/#repeating-spec-runs-and-managing-flaky-specs)

### Adding stress
You can run [stress](https://github.com/resurrecting-open-source-projects/stress) tool to increase CPU load during tests.  For example, if you're on Debian-based Linux:
```shell
# install stress:
sudo apt install stress
# run stress alongside tests
/usr/bin/stress --cpu 80
```

### Analyzing logs
Kueue runs as a regular pod on a worker node, and in e2e tests there are 2 replicas running.  The Kueue logs are located in `kind-worker/pods/kueue-system_kueue-controller-manager*/manager` and `kind-worker2/pods/kueue-system_kueue-controller-manager*/manager` folders.

For each log message you can from which file and line the message is coming from:
```log
2025-02-03T15:51:51.502425029Z stderr F 2025-02-03T15:51:51.502117824Z	LEVEL(-2)	cluster-queue-reconciler	core/clusterqueue_controller.go:341	ClusterQueue update event	{"clusterQueue": {"name":"cluster-queue"}}
```
Here, it's `core/clusterqueue_controller.go:341`.

### See also
- [Kubernetes testing guide](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-testing/testing.md)
- [Integration Testing in Kubernetes](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-testing/integration-tests.md)
- [End-to-End Testing in Kubernetes](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-testing/e2e-tests.md)
- [Flaky Tests in Kubernetes](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-testing/flaky-tests.md)
