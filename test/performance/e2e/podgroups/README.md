# Kueue Pod Integration e2e Performance Testing
## Introduction
A minimal setup for performance testing Plain Pods integration using
clusterloader2.

## Setup

Install Kueue, configured to use [Plain Pods](https://kueue.sigs.k8s.io/docs/tasks/run_plain_pods/).

e.g.
```
wget https://github.com/kubernetes-sigs/kueue/releases/download/v0.6.2/manifests.yaml

# manifest.diff is provided in this directory
patch manifests.yaml manifest.diff
kubectl apply --server-side -f manifests.yaml
```

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
