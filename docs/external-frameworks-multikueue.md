# MultiKueue External Frameworks Support

This document explains how to use MultiKueue with external frameworks that are not natively supported by Kueue.

## Overview

MultiKueue External Frameworks Support allows you to configure MultiKueue to work with any Custom Resource (CR) that follows a Job-like pattern. This is particularly useful for integrating CRDs such as:

- Tekton `PipelineRun`
- Argo `Workflow`
- Other custom job types

### Requirements for Custom Resources

To be managed by the generic MultiKueue adapter, a Custom Resource must have a `.spec.managedBy` field. When a CR is intended to be managed by MultiKueue, this field must be set to `"kueue.x-k8s.io/multikueue"`. The adapter uses this field to identify which objects to manage.

## Feature Gate

This feature is controlled by the `MultiKueueAdaptersForCustomJobs` feature gate:

- **Alpha (v0.14)**: Disabled by default

To enable the feature, add the `--feature-gates=MultiKueueAdaptersForCustomJobs=true` flag to the Kueue controller manager's arguments. For example, you can apply it to your deployment with the following command:

```bash
kubectl patch deployment kueue-controller-manager -n kueue-system \
  --type='json' -p='[{"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--feature-gates=MultiKueueAdaptersForCustomJobs=true"}]'
```

## Configuration

External frameworks are configured in the Kueue `Configuration` object. The settings are located under `data.multikueue.externalFrameworks`. This field holds a list of frameworks to be enabled.

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
data:
  multikueue:
    externalFrameworks:
      - name: "Kind.version.group"
      - name: "AnotherKind.version.group"
```

### Configuration Fields

Each entry in the `externalFrameworks` list is an object with the following field:

| Field      | Type   | Required | Description                                       |
|------------|--------|----------|---------------------------------------------------|
| `name`     | string | Yes      | GVK of the resource in the format `Kind.version.group`. |

### Example: Tekton PipelineRun

To demonstrate how to configure the adapter, let's use Tekton `PipelineRun` as an example.

First, update your Kueue configuration to include `PipelineRun` in the `externalFrameworks` list:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
data:
  multikueue:
    externalFrameworks:
      - name: "PipelineRun.v1.tekton.dev"
```

Once configured, the generic MultiKueue adapter will watch for `PipelineRun` resources that have `.spec.managedBy` set to `"kueue.x-k8s.io/multikueue"` and manage them as it does with other supported job types. This allows Kueue to handle resource management for `PipelineRun` objects across multiple clusters.

### RBAC

Ensure the Kueue controller has permissions for your external GVK. Example for Tekton PipelineRuns:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kueue-external-frameworks
rules:
- apiGroups: ["tekton.dev"]
  resources: ["pipelineruns", "pipelineruns/status"]
  verbs: ["get", "list", "watch", "patch", "update"]
+```
