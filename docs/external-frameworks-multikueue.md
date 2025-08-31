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

The minimal configuration requires only the Group, Version, and Kind:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
metadata:
  name: kueue-manager-config
  namespace: kueue-system
data:
  multikueue:
    externalFrameworks:
      - group: "tekton.dev"
        version: "v1"
        kind: "PipelineRun"
```

This configuration will use the following defaults:
- `managedBy`: `.spec.managedBy`
- `creationPatches`: Replace `/spec/managedBy` with `null`
- `syncPatches`: Copy entire `/status` from remote to local

### Advanced Configuration

You can customize the behavior with additional fields:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
metadata:
  name: kueue-manager-config
  namespace: kueue-system
data:
  multikueue:
    externalFrameworks:
      - group: "tekton.dev"
        version: "v1"
        kind: "PipelineRun"
        managedBy: ".spec.managedBy"
        creationPatches:
          - op: replace
            path: /spec/managedBy
            value: null
          - op: replace
            path: /spec/pipelineRef
            value:
              name: "remote-pipeline"
        syncPatches:
          - op: replace
            path: /status
            from: /status
          - op: replace
            path: /status/conditions
            from: /status/conditions
```

## Configuration Fields

### ExternalFramework

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `group` | string | Yes | API group of the custom resource |
| `version` | string | Yes | API version of the custom resource |
| `kind` | string | Yes | Kind of the custom resource |
| `managedBy` | string | No | JSONPath to the managedBy field (default: `.spec.managedBy`) |
| `creationPatches` | []JsonPatch | No | Patches to apply when creating in worker cluster |
| `syncPatches` | []JsonPatch | No | Patches to apply when syncing status |

### JsonPatch

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `op` | string | Yes | Operation: `add`, `remove`, `replace`, `move`, `copy`, `test` |
| `path` | string | Yes | JSON pointer path to target location |
| `value` | interface{} | No | Value for `add` and `replace` operations |
| `from` | string | No | Source path for `move` and `copy` operations |

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
      - group: "tekton.dev"
        version: "v1"
        kind: "PipelineRun"

---
# PipelineRun with MultiKueue
apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: example-pipeline
  namespace: default
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  managedBy: "kueue.x-k8s.io/multikueue"
  pipelineRef:
    name: example-pipeline
```

### Custom Job with Different managedBy Path

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
      - group: "custom.example.com"
        version: "v1alpha1"
        kind: "CustomJob"
        managedBy: ".metadata.labels.managedBy"
        creationPatches:
          - op: replace
            path: /metadata/labels/managedBy
            value: null
        syncPatches:
          - op: replace
            path: /status
            from: /status

---
# Custom Job
apiVersion: custom.example.com/v1alpha1
kind: CustomJob
metadata:
  name: example-job
  namespace: default
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    managedBy: "kueue.x-k8s.io/multikueue"
spec:
  # ... job specification
```

## How It Works

1. **Detection**: When a workload is created for a configured GVK, the generic adapter checks if the object is managed by Kueue using the `managedBy` field.

2. **Creation**: When the workload is admitted, the generic adapter:
   - Creates a copy of the object in the worker cluster
   - Applies the `creationPatches` to modify the object for the worker cluster
   - Adds MultiKueue labels for tracking

3. **Status Sync**: The generic adapter periodically:
   - Fetches the status from the worker cluster
   - Applies the `syncPatches` to update the local object's status

4. **Cleanup**: When the workload is deleted, the generic adapter removes the object from the worker cluster.

## Limitations

- The generic adapter only supports simple JSON patch operations
- Complex transformations require custom patches
- The `managedBy` field must be accessible via JSONPath
- Status synchronization is limited to the configured patches

## Troubleshooting

### Common Issues

1. **Object not managed by Kueue**
   - Ensure the `managedBy` field is set to `"kueue.x-k8s.io/multikueue"`
   - Check that the `managedBy` path in configuration is correct

2. **Patch application failures**
   - Verify JSON patch syntax
   - Ensure paths exist in the object structure
   - Check that patch operations are valid for the target paths

3. **Status not syncing**
   - Verify `syncPatches` configuration
   - Check that remote object has the expected status structure
   - Ensure worker cluster connectivity

### Debugging

Enable debug logging to see detailed information about adapter operations:

```bash
kubectl logs -n kueue-system deployment/kueue-controller-manager -f --v=4
```

Look for log messages containing:
- `generic adapter`
- `external framework`
- `patch application`
- `status sync`

## Migration from Built-in Adapters

If you're currently using a built-in adapter and want to switch to the generic adapter:

1. Add the external framework configuration
2. Ensure the same GVK is not configured in both places
3. Test with a small subset of workloads
4. Monitor for any differences in behavior
5. Gradually migrate all workloads

## Best Practices

1. **Start Simple**: Begin with minimal configuration and add complexity as needed
2. **Test Thoroughly**: Test patches with sample objects before production use
3. **Monitor Performance**: Generic adapters may have different performance characteristics
4. **Document Configuration**: Keep detailed documentation of your external framework configurations
5. **Version Control**: Store configurations in version control for reproducibility
