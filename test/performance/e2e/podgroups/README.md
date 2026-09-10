# Kueue Pod Integration e2e Performance Testing
## Introduction
A minimal setup for performance testing Plain Pods integration using
clusterloader2.

## Setup

Install a current Kueue release with the
[Plain Pods](https://kueue.sigs.k8s.io/docs/tasks/run/plain_pods/) integration
enabled. The Kueue resource manifests in these performance tests use the
`kueue.x-k8s.io/v1beta2` API.

Install `Clusterloader2`:
  * checkout https://github.com/kubernetes/perf-tests
  * change to `clusterloader2` directory
  * run `go build -o clusterloader './cmd/'`

Then, set the env variable `CL2_HOME_DIR` to the clusterloader2 directory.

PROVIDER and KUBECONFIG env variables may also be overridden.

## Running

```
CL2_HOME_DIR="/path/to/your/clusterloader" ./run-test.sh
```

Parameters are configurable in test-config.yaml. Apart from clusterqueues,
resources are created in the kueue-pod-performance-1 namespace.

| Parameter         | Description |
| -----------       | ----------- |
| PODS_TOTAL_QUOTA  | Total number of pods Kueue will admit at a single time |
| QUEUES            | Number of ClusterQueues. `PODS_TOTAL_QUOTA` is evenly divided between these queues |
| COHORTS           | Number of Cohorts. ClusterQueues are evenly divided between theese cohorts |
| BORROW_RATIO      | Fraction of nominal quota a ClusterQueue may borrow from its Cohort |
| PODS              | Number of pods to create during the test |
| WORKLOADS         | Number of workloads; pods are divided evenly between workloads |



## Cleanup
As long as the test is not interrupted (e.g. via SIGINT), this cleanup happens
automatically. To ensure all resources are cleaned up, you may run:

```
kubectl delete namespace/kueue-pod-performance-1
kubectl delete clusterqueue -l "group=pod-performance-cluster-queue"
kubectl delete -f templates/resource-flavor.yaml
```
