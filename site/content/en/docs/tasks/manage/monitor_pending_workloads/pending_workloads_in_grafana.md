---
title: "Pending Workloads in Grafana"
date: 2025-06-12
weight: 20
description: >
  Monitoring Pending Workloads using the VisibilityOnDemand feature in Grafana.
---

This guide explains how to monitor pending workloads in Grafana using the `VisibilityOnDemand` feature.

The intended audience for this page are [batch administrators](/docs/tasks#batch-administrator)
for ClusterQueue visibility, and [batch users](/docs/tasks#batch-user) for LocalQueue visibility.

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- [Kueue](/docs/installation) is installed
- [Kube-prometheus operator](https://github.com/prometheus-operator/kube-prometheus/blob/main/README.md#quickstart) is installed in version v0.15.0 or later.
- The [VisibilityOnDemand](/docs/tasks/manage/monitor_pending_workloads/pending_workloads_on_demand/#monitor-pending-workloads-on-demand) feature is enabled.

## Setting Up Grafana for Pending Workloads

### Step 1: Configure Cluster Permissions

To enable visibility, create a `ClusterRole` and `ClusterRoleBinding` for either `ClusterQueue` or `LocalQueue`:

- For `ClusterQueue` visibility:

{{< include "examples/visibility/grafana-cluster-queue-reader.yaml" "yaml" >}}

- For `LocalQueue` visibility:

{{< include "examples/visibility/grafana-local-queue-reader.yaml" "yaml" >}}

Apply the appropriate configuration:

```shell
kubectl apply -f <filename>.yaml
```

### Step 2: Generate a Service Account Token

Create a token for Grafana authentication:

```shell
TOKEN=$(kubectl create token default -n default)
echo $TOKEN
```

Save the token for use in Step 5.

### Step 3: Set up port forwarding for Grafana

Access Grafana locally:

```shell
kubectl port-forward -n monitoring service/grafana 3000:3000
```

Grafana is now available at [http://localhost:3000](http://localhost:3000).

### Step 4: Install the Infinity Plugin

1. Open Grafana at [http://localhost:3000](http://localhost:3000).
2. Log in (default credentials: admin/admin).
3. Go to `Connections` > `Add new connection`. 
4. Search for `Infinity` and click `Install`.

### Step 5: Configure the Infinity Data Source

1. Go to `Connections` >` Data sources` and click `+ Add new data source`.
2. Select `Infinity`.
3. Configure the data source:
    - Authentication: Set the `Bearer Token` to the token generated in Step 2.
    - Network: Enable `Skip TLS Verify`.
    - Security: Add `https://kubernetes.default.svc` to allowed hosts and set `Query security` to `Allowed`.
4. Click `Save & test` to verify the configuration.


### Step 6: Import the Pending Workloads Dashboard

1. Download the appropriate dashboard JSON:
   - [ClusterQueue Visibility](/examples/visibility/pending-workloads-for-cluster-queue-visibility-dashboard.json).
   - [LocalQueue Visibility](/examples/visibility/pending-workloads-for-local-queue-visibility-dashboard.json).
2. In Grafana, go to `Dashboards` > `New` > `Import`.
3. Select `Upload dashboard JSON` file and choose the downloaded file.
4. Select the Infinity data source configured in Step 5.
5. Click `Import`.

### Step 7: Set Up ClusterQueue

To configure a basic `ClusterQueue`, apply the following:

{{< include "examples/admin/single-clusterqueue-setup.yaml" "yaml" >}}

Apply the configuration:

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/admin/single-clusterqueue-setup.yaml
```

### Step 8: Create Sample Workloads

To populate the dashboard with data, create sample jobs:

{{< include "examples/jobs/sample-job.yaml" "yaml" >}}

Apply the job multiple times:

```shell
for i in {1..6}; do kubectl create -f https://kueue.sigs.k8s.io/examples/jobs/sample-job.yaml; done
```

### Step 9: View the Dashboard

1. In Grafana, navigate to `Dashboards`.
2. Select the imported dashboard (e.g., "Pending Workloads for ClusterQueue visibility").
3. Verify that pending workloads are displayed.

![ClusterQueue Visibility Dashboard](/images/pending-workloads-for-cluster-queue-visibility-dashboard.png)

![LocalQueue Visibility Dashboard](/images/pending-workloads-for-local-queue-visibility-dashboard.png)

## Troubleshooting

### No data in dashboard

Ensure jobs are created and the `Infinity` data source is correctly configured.

### Permission errors

Verify the `ClusterRole` and `ClusterRoleBinding` are applied correctly.

### Grafana inaccessible

Check port forwarding and ensure the Grafana service is running in the monitoring namespace.
