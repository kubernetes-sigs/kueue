# Kueue Pod Integration Performance Testing
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

Install and build clusterloader - see https://github.com/kubernetes-sigs/kueue/tree/main/test/performance#how-to-run-the-test
Then, set the env variable `CL2_HOME_DIR` to its location.

PROVIDER and KUBECONFIG env variables may also be overridden.

## Running
Parameters are configurable in test-config.yaml. Apart from clusterqueues,
resources are created in the kueue-pod-performance-1 namespace.

```
CL2_HOME_DIR="/path/to/your/clusterloader" ./run-test.sh
```

## Cleanup
```
kubectl delete namespace/kueue-pod-performance-1
kubectl delete clusterqueue -l "group=pod-performance-cluster-queue"
kubectl delete -f templates/resource-flavor.yaml
```
