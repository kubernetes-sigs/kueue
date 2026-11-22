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
- [Kueue](/docs/installation) v0.15.0 or later is installed.
- Prometheus Operator is installed. See [Setup Prometheus](/docs/tasks/manage/observability/setup_prometheus) for instructions.
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

## Use Grafana Cloud

The Prometheus metrics, such as `kueue_pending_workloads`, report the number of pending Workloads.
They do not report the position of each Workload in a queue. That information comes from the
visibility API, which Grafana can only query if it reaches the Kubernetes API server.

Grafana Cloud runs outside your cluster, so it cannot reach `https://kubernetes.default.svc` directly.
Use [private data source connect (PDC)](https://grafana.com/docs/grafana-cloud/connect-externally-hosted/private-data-source-connect/)
to route the Infinity data source queries through an agent running inside the cluster. PDC opens an outbound
connection from the agent, so you do not need to expose the Kubernetes API server to the internet.

Before you begin, make sure the [prerequisites](#before-you-begin) are met, except for Prometheus Operator.
You also need an administrator account for your Grafana Cloud organization.

1. Complete [Step 1](#step-1-configure-cluster-permissions) and [Step 2](#step-2-generate-a-service-account-token).
   The token created by `kubectl create token` expires, by default after one hour. The `--duration` flag only
   requests a lifetime, and the API server can issue a shorter one. To check when the token you received expires,
   run:

   ```shell
   echo "$TOKEN" | cut -d. -f2 | tr '_-' '/+' | jq -R '@base64d | fromjson | .exp | todate'
   ```

   When the token expires, the Infinity queries fail and the dashboard stops showing queue positions. To renew it,
   run `kubectl create token` again and replace the `Bearer Token` in the Infinity data source. To avoid renewing,
   use a [long-lived ServiceAccount token](https://kubernetes.io/docs/reference/access-authn-authz/service-accounts-admin/#manually-create-a-long-lived-api-token-for-a-serviceaccount),
   which does not expire.

2. Deploy a PDC agent in the cluster by following
   [Configure private data source connect](https://grafana.com/docs/grafana-cloud/connect-externally-hosted/private-data-source-connect/configure-pdc/#deploy-the-pdc-agent).
   The agent must run in the cluster so that it can resolve `kubernetes.default.svc`.

   {{% alert title="Note" color="primary" %}}
   By default, Grafana Cloud can connect to any host reachable from the PDC agent. Restrict the agent to the
   Kubernetes API server with the `-ssh-flag='-o PermitRemoteOpen=kubernetes.default.svc:443'` flag. For details, see
   [Restrict accessible endpoints](https://grafana.com/docs/grafana-cloud/connect-externally-hosted/private-data-source-connect/configure-pdc/#restrict-accessible-endpoints).
   {{% /alert %}}

3. In Grafana Cloud, install the [Infinity data source plugin](https://grafana.com/docs/plugins/yesoreyeram-infinity-datasource/latest/)
   and add an Infinity data source with the following settings:
    - Authentication: Set the `Bearer Token` to the token generated in step 1.
    - Allowed hosts: Add `https://kubernetes.default.svc`.
    - TLS: Enable `With CA cert`, set `Server name` to `kubernetes.default.svc`, and provide the cluster CA
      certificate, which you can print with the following command:

      ```shell
      kubectl get configmap kube-root-ca.crt -n default -o jsonpath='{.data.ca\.crt}'
      ```

      Verifying the certificate prevents an impersonated API server from receiving the bearer token. Use
      `Skip TLS Verify` only temporarily to troubleshoot.

    - Private data source connect: Select the PDC connection created in step 2.
    - Health check: In the `Health check` section, enable the custom health check and set the URL to
      `https://kubernetes.default.svc/version`. The health check passes when the URL returns HTTP 200.

   Click `Save & test` to verify the configuration. Without a custom health check, `Save & test` does not send a
   request to the Kubernetes API server, so it does not confirm that Grafana Cloud reaches the API server through
   the PDC agent.

4. Complete [Step 6](#step-6-import-the-pending-workloads-dashboard) in Grafana Cloud, selecting the data source
   from the previous step. Then, create sample Workloads as described in
   [Step 7](#step-7-set-up-clusterqueue) and [Step 8](#step-8-create-sample-workloads), and
   [view the dashboard](#step-9-view-the-dashboard).

You can keep the existing Prometheus-based dashboards in Grafana Cloud. See
[Common Grafana queries](/docs/tasks/manage/observability/common_grafana_queries) for queries that
use the Kueue metrics.

## Troubleshooting

### No data in dashboard

Ensure jobs are created and the `Infinity` data source is correctly configured.

### Permission errors

Verify the `ClusterRole` and `ClusterRoleBinding` are applied correctly.

### Grafana inaccessible

Check port forwarding and ensure the Grafana service is running in the monitoring namespace.
