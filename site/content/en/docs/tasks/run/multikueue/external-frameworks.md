---
title: "Run External Framework Jobs in Multi-Cluster"
linkTitle: "External Framework Jobs"
weight: 5
date: 2025-09-19
description: >
  Run a MultiKueue scheduled External Framework Job.
---

{{< feature-state state="alpha" for_version="v0.14" >}}

## Before you begin

1. Check the [MultiKueue installation guide](/docs/tasks/manage/setup_multikueue) on how to properly setup MultiKueue clusters.

2. Enable the `MultiKueueAdaptersForCustomJobs` feature gate. This feature is in Alpha and is disabled by default.

   To enable the feature, add the `--feature-gates=MultiKueueAdaptersForCustomJobs=true` flag to the Kueue controller manager's arguments. For example, you can apply it to your deployment with the following command:

   ```bash
   kubectl patch deployment kueue-controller-manager -n kueue-system \
     --type='json' -p='[{"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--feature-gates=MultiKueueAdaptersForCustomJobs=true"}]'
   ```

3. Ensure the Kueue controller has permissions for your external GVK. Example for Tekton PipelineRuns:

   ```yaml
   apiVersion: rbac.authorization.k8s.io/v1
   kind: ClusterRole
   metadata:
     name: kueue-external-frameworks
   rules:
   - apiGroups: ["tekton.dev"]
     resources: ["pipelineruns", "pipelineruns/status"]
     verbs: ["get", "list", "watch", "patch", "update"]
   ```

## MultiKueue integration

MultiKueue External Frameworks Support allows you to configure MultiKueue to work with any Custom Resource (CR) that follows a Job-like pattern. This is particularly useful for integrating CRDs such as:

- Tekton `PipelineRun`
- Argo `Workflow`
- Other custom job types

To be managed by the generic MultiKueue adapter, a Custom Resource must have a `.spec.managedBy` field. When a CR is intended to be managed by MultiKueue, this field must be set to `"kueue.x-k8s.io/multikueue"`. The adapter uses this field to identify which objects to manage.

External frameworks are configured in the Kueue `Configuration` object. The settings are located under `multikueue.externalFrameworks`. This field holds a list of frameworks to be enabled.

Each entry in the `externalFrameworks` list is an object with the following field:

| Field      | Type   | Required | Description                                       |
|------------|--------|----------|---------------------------------------------------|
| `name`     | string | Yes      | GVK of the resource in the format `Kind.version.group`. |

## Example: Tekton PipelineRun

To demonstrate how to configure the adapter, let's use Tekton `PipelineRun` as an example.

First, update your Kueue configuration to include `PipelineRun` in the `externalFrameworks` list:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
data:
  multikueue:
    externalFrameworks:
      - name: "PipelineRun.v1.tekton.dev"
```

Once configured, the generic MultiKueue adapter will watch for `PipelineRun` resources that have `.spec.managedBy` set to `"kueue.x-k8s.io/multikueue"` and manage them as it does with other supported job types. This allows Kueue to handle resource management for `PipelineRun` objects across multiple clusters.

{{% alert title="Note" color="primary" %}}
Note: Kueue defaults the `spec.managedBy` field to `kueue.x-k8s.io/multikueue` on the management cluster for External Framework Jobs.

This allows the External Framework controller to ignore the Jobs managed by MultiKueue on the management cluster, and in particular skip creation of the wrapped resources.

The resources are created and the actual computation will happen on the mirror copy of the External Framework Job on the selected worker cluster.
The mirror copy of the External Framework Job does not have the field set.
{{% /alert %}}
