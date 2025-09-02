# MultiKueue External Frameworks Support

This document explains how to use MultiKueue with external frameworks that are not natively supported by Kueue.

## Overview

MultiKueue External Frameworks Support allows you to configure MultiKueue to work with any Custom Resource (CR) that follows the standard Kubernetes patterns. This feature is particularly useful for:

- Tekton PipelineRun
- Argo Workflows
- Custom job types
- Any CR that has a `managedBy` field or similar mechanism

## Feature Gate

This feature is controlled by the `MultiKueueAdaptersForCustomJobs` feature gate:

- **Alpha (v0.14)**: Disabled by default
- **Beta (v0.15)**: Enabled by default
- **GA**: Feature gate removed

To enable the feature:

```bash
# Enable the feature gate
kubectl patch deployment kueue-controller-manager -n kueue-system \
  --type='json' -p='[{"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--feature-gates=MultiKueueAdaptersForCustomJobs=true"}]'
```

## Configuration

External frameworks are configured in the Kueue configuration under the `multikueue.externalFrameworks` section.

### Basic Configuration

The configuration requires only the GVK in the format `Kind.version.group.com`:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
metadata:
  name: kueue-manager-config
  namespace: kueue-system
data:
  multikueue:
    externalFrameworks:
      - name: "PipelineRun.v1.tekton.dev"
```

This configuration will use the following defaults:
- `managedBy`: `.spec.managedBy` (hardcoded)
- Creation: Remove `.spec.managedBy` field
- Status sync: Copy entire `/status` from remote to local

## Configuration Fields

### MultiKueueExternalFramework

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | GVK in format `Kind.version.group.com` |

## Usage Examples

### Tekton PipelineRun

```yaml
# Configuration
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
metadata:
  name: kueue-manager-config
  namespace: kueue-system
data:
  multikueue:
    externalFrameworks:
      - name: "PipelineRun.v1.tekton.dev"
```

### Custom Job with Different managedBy Path

```