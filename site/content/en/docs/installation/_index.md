---
title: "Installation"
linkTitle: "Installation"
weight: 2
description: >
  Installing Kueue to a Kubernetes Cluster
---

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster with version 1.21 or newer is running. Learn how to [install the Kubernetes tools](https://kubernetes.io/docs/tasks/tools/).
- The `SuspendJob` [feature gate][feature_gate] is enabled. In Kubernetes 1.22 or newer, the feature gate is enabled by default.
- (Optional) The `JobMutableNodeSchedulingDirectives` [feature gate][feature_gate] (available in Kubernetes 1.22 or newer) is enabled.
  In Kubernetes 1.23 or newer, the feature gate is enabled by default.
- The kubectl command-line tool has communication with your cluster.

Kueue publishes [metrics](/docs/reference/metrics) to monitor its operators.
You can scrape these metrics with Prometheus.
Use [kube-prometheus](https://github.com/prometheus-operator/kube-prometheus)
if you don't have your own monitoring system.

The webhook server in kueue uses an internal cert management for provisioning certificates. If you want to use
  a third-party one, e.g. [cert-manager](https://github.com/cert-manager/cert-manager), follow these steps:

  1. Set `internalCertManagement.enable` to `false` in [config file](#install-a-custom-configured-released-version).
  2. Comment out the `internalcert` folder in `config/default/kustomization.yaml`.
  3. Enable `cert-manager` in `config/default/kustomization.yaml` and uncomment all sections with 'CERTMANAGER'.

[feature_gate]: https://kubernetes.io/docs/reference/command-line-tools-reference/feature-gates/

## Install a released version

To install a released version of Kueue in your cluster, run the following command:

```shell
VERSION={{< param "version" >}}
kubectl apply -f https://github.com/kubernetes-sigs/kueue/releases/download/$VERSION/manifests.yaml
```

### Add metrics scraping for prometheus-operator

> _Available in Kueue v0.2.1 and later_

To allow [prometheus-operator](https://github.com/prometheus-operator/prometheus-operator)
to scrape metrics from kueue components, run the following command:

```shell
kubectl apply -f https://github.com/kubernetes-sigs/kueue/releases/download/$VERSION/prometheus.yaml
```

### Uninstall

To uninstall a released version of Kueue from your cluster, run the following command:

```shell
VERSION={{< param "version" >}}
kubectl delete -f https://github.com/kubernetes-sigs/kueue/releases/download/$VERSION/manifests.yaml
```

## Install a custom-configured released version

To install a custom-configured released version of Kueue in your cluster, execute the following steps:

1. Download the release's `manifests.yaml` file:

    ```shell
    VERSION={{< param "version" >}}
    wget https://github.com/kubernetes-sigs/kueue/releases/download/$VERSION/manifests.yaml
    ```

2. With an editor of your preference, open `manifests.yaml`.
3. In the `kueue-manager-config` ConfigMap manifest, edit the
`controller_manager_config.yaml` data entry. The entry represents
the default Kueue Configuration
struct ([v1beta1@main](https://pkg.go.dev/sigs.k8s.io/kueue@main/apis/config/v1beta1#Configuration)).
The contents of the ConfigMap are similar to the following:

> __The `namespace` and `internalCertManagement` fields are available in Kueue v0.3.0 and later__

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kueue-manager-config
  namespace: kueue-system
data:
  controller_manager_config.yaml: |
    apiVersion: config.kueue.x-k8s.io/v1beta1
    kind: Configuration
    namespace: kueue-system
    health:
      healthProbeBindAddress: :8081
    metrics:
      bindAddress: :8080
    webhook:
      port: 9443
    manageJobsWithoutQueueName: true
    internalCertManagement:
      enable: true
      webhookServiceName: kueue-webhook-service
      webhookSecretName: kueue-webhook-server-cert
    waitForPodsReady:
      enable: true
      timeout: 10m
    # pprofBindAddress: :8082
    integrations:
      frameworks:
      - "batch/job"
    # - "kubeflow.org/mpijob"
    # - "ray.io/rayjob"
```

__The `namespace`, `waitForPodsReady`, and `internalCertManagement` fields are available in Kueue v0.3.0 and later__

> **Note**
> See [Sequential Admission with Ready Pods](/docs/tasks/setup_sequential_admission) to learn
more about using `waitForPodsReady` for Kueue.

4. Apply the customized manifests to the cluster:

```shell
kubectl apply -f manifests.yaml
```

## Change the feature gates configuration

Kueue uses a similar mechanism to configure features as described in [Kubernetes Feature Gates](https://kubernetes.io/docs/reference/command-line-tools-reference/feature-gates).

In order to change the default of a feature, you need to edit the `kueue-controller-manager` deployment within the kueue installation namespace and change the `manager` container arguments to include 

```
--feature-gates=...,<FeatureName>=<true|false>
```

For example, to enable `PartialAdmission`, you should change the manager deployment as follows:

```diff
kind: Deployment
...
spec:
  ...
  template:
    ...
    spec:
      containers:
      - name: manager
        args:
        - --config=controller_manager_config.yaml
        - --zap-log-level=2
+       - --feature-gates=PartialAdmission=true
```

The currently supported features are:

| Feature | Default | Stage | Since | Until |
|---------|---------|-------|-------|-------|
| `PartialAdmission` | `false` | Alpha | 0.4 |  |

## Install the latest development version

To install the latest development version of Kueue in your cluster, run the
following command:

```shell
kubectl apply -k "github.com/kubernetes-sigs/kueue/config/default?ref=main"
```

The controller runs in the `kueue-system` namespace.

### Uninstall

To uninstall Kueue, run the following command:

```shell
kubectl delete -k "github.com/kubernetes-sigs/kueue/config/default?ref=main"
```

## Build and install from source

To build Kueue from source and install Kueue in your cluster, run the following
commands:

```sh
git clone https://github.com/kubernetes-sigs/kueue.git
cd kueue
IMAGE_REGISTRY=registry.example.com/my-user make image-local-push deploy
```

### Add metrics scraping for prometheus-operator

> _Available in Kueue v0.2.0 and later_

To allow [prometheus-operator](https://github.com/prometheus-operator/prometheus-operator)
to scrape metrics from kueue components, run the following command:

```shell
make prometheus
```

### Uninstall

To uninstall Kueue, run the following command:

```sh
make undeploy
```

## Enabling pprof endpoints

> _Available in Kueue v0.4.0 and later_

It's available only if `pprofBindAddress` is set in the [manager's configuration](/docs/installation/#install-a-custom-configured-released-version).

The easiest way to reach pprof port in kubernetes it's to use `port-forward` command:

```shell
kubectl port-forward kueue-controller-manager-769f96b5dc-87sf2 -n kueue-system 8082:8082
Forwarding from 127.0.0.1:8082 -> 8082
Forwarding from [::1]:8082 -> 8082
```

The HTTP endpoint will now be available as a local port.
For example, you can now generate the file for the CPU profile with curl and pipe the data to a file (600 seconds is 10 minutes)

```shell
curl "http://localhost:8082/debug/pprof/profile?seconds=600" > cpu.pprof
```

For more details please follow [the pprof documentation](https://github.com/google/pprof#building-pprof)
