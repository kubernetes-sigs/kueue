---
title: "Monitor actual resource usage"
date: 2026-02-24
weight: 3
description: >
  Monitor actual resource usage with aggregation or filtering by specific queue.
---

This page shows you how to use Kueue labels assigned to pods to monitor resource
usage.

The intended audience for this page are [batch administrators](/docs/tasks#batch-administrator).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation).
- The cluster has at least one [local queue and cluster queue](/docs/tasks/manage/administer_cluster_quotas). Presented commands assume your local queue is named "user-queue" and cluster queue is named "cluster-queue", if this is not the case adjust them appropriately.


{{% alert title="Note" color="primary" %}}
The queries in this page require `AssignQueueLabelsForPods` Feature Gate, which is enabled by default.
If it is not enabled, see [Installation](/docs/installation/#change-the-feature-gates-configuration) for details how to enable it.
{{% /alert %}}

## `kubectl top` for command line resource usage debugging

{{% alert title="Warning" color="warning" %}}
As mentioned on  [Metrics Server repository site](https://github.com/kubernetes-sigs/metrics-server?tab=readme-ov-file#kubernetes-metrics-server),
Metrics Server and provided command line tool - `kubectl top` - is not meant as a monitoring solution. The tool is convenient
for quick troubleshooting but is not a substitute for full-scale monitoring. See the [Production resource monitoring](#production-resource-monitoring) section for a more robust setup.

{{% /alert %}}

1. To use `kubectl top` you need to install metrics-server for you cluster.
Follow the [Metrics Server Installation](https://github.com/kubernetes-sigs/metrics-server?tab=readme-ov-file#installation)
2. Schedule a couple of jobs that have some actual cpu usage to your local queue:
```sh
for i in {1..3}; do
kubectl create -f - <<'EOF'
apiVersion: batch/v1
kind: Job
metadata:
  generateName: sample-job-
  namespace: default
  labels:
    kueue.x-k8s.io/queue-name: "user-queue"
spec:
  parallelism: 1
  completions: 1
  template:
    spec:
      containers:
      - name: dummy-job
        image: registry.k8s.io/e2e-test-images/agnhost:2.53
        command: [ "/bin/sh" ]
        args: [ "-c", "end_time=$(($(date +%s) + 60)); while [ $(date +%s) -lt $end_time ]; do :; done"]
        resources:
          requests:
            cpu: "1"

      restartPolicy: Never
EOF
done
```
3. Monitor the usage of cpu and memory in nearly real time for this local queue with `kubectl top`:
```sh
watch -n 15  'kubectl top pod --sum -l kueue.x-k8s.io/local-queue-name=user-queue'
```
You will see a list of pods currently running on the local queue named "user-queue" with their current cpu and memory measurements and the sum of the usage at the bottom. The list will be automatically refreshed every 15 seconds.
The outcome should look like this:
```text
Every 15.0s: kubectl top pod --sum -l kueue.x-k8s.io/local-queue-name=user-queue

NAME                     CPU(cores)   MEMORY(bytes)
sample-job-jd2vm-9hpnr   661m         3Mi
sample-job-p8mxr-cld27   684m         2Mi
sample-job-wccrt-h4684   654m         0Mi
                         ________     ________
                         1998m        6Mi
```
4. Similarly, you can monitor the usage of the resources by jobs admitted to a cluster queue:
```sh
watch -n 15  'kubectl top pod --sum -l kueue.x-k8s.io/cluster-queue-name=cluster-queue'
```

{{% alert title="Note" color="primary" %}}
If you encounter an error like "error: Metrics API not available", it may be caused by certificate
verification problems in your cluster. You can skip the verification in metrics-server by
editing the deployment `kubectl edit deployment metrics-server -n kube-system`
and adding `--kubelet-insecure-tls` to the container arguments, however this
is **highly discouraged in production environments**.
{{% /alert %}}


## Production resource monitoring

1. Install [prometheus](/docs/tasks/manage/observability/setup_prometheus) and [kube-state-metrics](https://github.com/kubernetes/kube-state-metrics) according to your cluster's setup. Next, configure your `kube-state-metrics` to allowlist Kueue pod labels.

The commands below assume you are using the `kube-prometheus-stack` Helm chart (which includes both tools). If you use a different setup, adjust your configuration accordingly. To proceed with the Helm chart, add the following fragment to your `values.yaml` file:
```yaml
kube-state-metrics:
  metricLabelsAllowlist:
    - pods=[kueue.x-k8s.io/cluster-queue-name, kueue.x-k8s.io/local-queue-name]
```

Deploy prometheus stack helm chart with `values.yaml`:
```sh
helm install kube-prometheus-stack oci://ghcr.io/prometheus-community/charts/kube-prometheus-stack -f values.yaml
```
2. If you do not have the Prometheus UI available, forward a port for the Prometheus service:
```sh
kubectl port-forward svc/kube-prometheus-stack-prometheus 9090:9090
```
3. Deploy some jobs, such as the sample jobs defined earlier.

4. Verify that the new labels are available by running a PromQL query in the [Prometheus UI](http://localhost:9090/). (If you are not using port-forwarding, use your specific cluster address instead.)
```promql
kube_pod_labels{label_kueue_x_k8s_io_local_queue_name!=""}
```

5. You can now join the queue labels with your existing pod resource metrics using [group_left](https://prometheus.io/docs/prometheus/latest/querying/operators/#many-to-one-and-one-to-many-vector-matches). For example, use this query to aggregate CPU usage by local queue:
```
sum by (label_kueue_x_k8s_io_local_queue_name) (
  sum by (namespace, pod) (
    rate(container_cpu_usage_seconds_total{container!="", pod!=""}[5m])
  )
  * on(namespace, pod) group_left(label_kueue_x_k8s_io_local_queue_name)
  kube_pod_labels{label_kueue_x_k8s_io_local_queue_name!=""}
)
```

To filter or aggregate by cluster queue instead, replace the local queue label with   `label_kueue_x_k8s_io_cluster_queue_name`.
