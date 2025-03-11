---
title: "Running tests"
linkTitle: "Running tests"
weight: 25
description: >
  Running tests in Kueue
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

```shell
# install go stress
go install golang.org/x/tools/cmd/stress@latest
# recompile with race instrumentation
go test ./pkg/scheduler/preemption/ -race -c
# it loops and reports failures
stress ./preemption.test -test.run TestPreemption
```

## Running integration tests

For running subset of tests see [Running subset of tests](#running-subset-of-integration-or-e2e-tests).

### Increase logging verbosity
You can change log level in the framework (for example, change -3 to -5 to increase verbosity):
https://github.com/kubernetes-sigs/kueue/blob/f8015cb273f9115c34f9be32b35f7e1308c16459/test/integration/framework/framework.go#L72:
```go
var setupLogger = sync.OnceFunc(func() {
	ctrl.SetLogger(util.NewTestingLogger(ginkgo.GinkgoWriter, -3))
})
```

## Running e2e tests using custom build
```shell
make kind-image-build test-e2e
```

For running subset of tests see [Running subset of tests](#running-subset-of-integration-or-e2e-tests).

## Debug tests in VSCode
It is possible to debug unit and integration tests in VSCode.
You need to have the [Go extension](https://marketplace.visualstudio.com/items?itemName=golang.Go) installed.
Now you will have `run test | debug test` text buttons above lines like
```go
func TestValidateClusterQueue(t *testing.T) {
```
You can click on the `debug test` to debug a specific test.

For integration tests, an additional step is needed.  Run `make test-integration | grep KUBEBUILDER_ASSETS` and add the path to the variable in settings.json:
```json
"go.testEnvVars": {
    "KUBEBUILDER_ASSETS": "<path from output above>",
  },
```

## Running subset of integration or e2e tests
### Use Ginkgo --focus arg
```shell
GINKGO_ARGS="--focus=Scheduler" make test-integration
GINKGO_ARGS="--focus=Creating a Pod requesting TAS" make test-e2e
```
### Use ginkgo.FIt
If you want to focus on a specific tests, you can change
`ginkgo.It` to `ginkgo.FIt` for these tests [ref][https://onsi.github.io/ginkgo/#focused-specs].
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
You can use a script to repeatedly test:
```shell
echo "going to test $NUM_TEST_ITERATIONS times"
for i in $(seq 1 $NUM_TEST_ITERATIONS); do
  GINKGO_ARGS="--focus=Scheduler" make test-integration >> loop-test.log 2>&1
  echo iteration $i
  grep -q "FAIL!" loop-test.log
  if [ $? -eq 0 ]; then
    echo "Test run contains FAILED tests"
    exit 1
  fi
done
echo "looped the test $NUM_TEST_ITERATIONS times"
```

### Adding stress
You can run `stress` to increase CPU load during tests.
```shell
# install stress, for example with
sudo apt install stress
# run stress alongside tests
/usr/bin/stress --cpu 80
```

And you can add --race to Ginkgo arguments for e2e tests (on integration tests it's already used):
```shell
GINKGO_ARGS="--race --focus=Scheduler" make test-e2e
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
