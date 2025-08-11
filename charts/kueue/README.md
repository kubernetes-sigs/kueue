# kueue

![Version: 0.13.2](https://img.shields.io/badge/Version-0.13.2-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: v0.13.2](https://img.shields.io/badge/AppVersion-v0.13.2-informational?style=flat-square)

Kueue is a set of APIs and controller for job queueing. It is a job-level manager that decides when a job should be admitted to start (as in pods can be created) and when it should stop (as in active pods should be deleted).

### Installation

Quick start instructions for the setup and configuration of kueue using Helm.

#### Prerequisites

- [Helm](https://helm.sh/docs/intro/quickstart/#install-helm)
- (Optional) [Cert-manager](https://cert-manager.io/docs/installation/)

#### Installing the chart

##### Install chart using Helm v3.0+

Either clone the kueue repository:

```bash
$ git clone git@github.com:kubernetes-sigs/kueue.git
$ cd kueue/charts
$ helm install kueue kueue/ --create-namespace --namespace kueue-system
```

Or use the charts pushed to `oci://registry.k8s.io/kueue/charts/kueue`:

```bash
helm install kueue oci://registry.k8s.io/kueue/charts/kueue --version="0.13.2" --create-namespace --namespace=kueue-system
```

For more advanced parametrization of Kueue, we recommend using a local overrides file, passed via the `--values` flag. For example:

```yaml
controllerManager:
  featureGates:
    - name: TopologyAwareScheduling
      enabled: true
  replicas: 2
  manager:
    resources:
      limits:
        cpu: "2"
        memory: 2Gi
      requests:
        cpu: "2"
        memory: 2Gi
```

```bash
helm install kueue oci://registry.k8s.io/kueue/charts/kueue --version="0.13.2" \
  --create-namespace --namespace=kueue-system \
  --values overrides.yaml
```

You can also use the `--set` flag. For example, to enable a feature gate (e.g., `TopologyAwareScheduling`):

```bash
helm install kueue oci://registry.k8s.io/kueue/charts/kueue --version="0.13.2" \
  --create-namespace --namespace=kueue-system \
  --set "controllerManager.featureGates[0].name=TopologyAwareScheduling" \
  --set "controllerManager.featureGates[0].enabled=true"
```

##### Verify that controller pods are running properly.

```bash
$ kubectl get deploy -n kueue-system
NAME                           READY   UP-TO-DATE   AVAILABLE   AGE
kueue-controller-manager       1/1     1            1           7s
```

##### Cert Manager

Kueue has support for third-party certificates.
One can enable this by setting `enableCertManager` to true.
This will use certManager to generate a secret, inject the CABundles and set up the tls.

Check out the [site](https://kueue.sigs.k8s.io/docs/tasks/manage/productization/cert_manager/)
for more information on installing cert manager with our Helm chart.

##### Prometheus

Kueue supports prometheus metrics.
Check out the [site](https://kueue.sigs.k8s.io/docs/tasks/manage/productization/prometheus/)
for more information on installing kueue with metrics using our Helm chart.

### Configuration

The following table lists the configurable parameters of the kueue chart and their default values.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| controllerManager.featureGates | list | `[]` | ControllerManager's feature gates |
| controllerManager.imagePullSecrets | list | `[]` | ControllerManager's imagePullSecrets |
| controllerManager.livenessProbe.failureThreshold | int | `3` | ControllerManager's livenessProbe failureThreshold |
| controllerManager.livenessProbe.initialDelaySeconds | int | `15` | ControllerManager's livenessProbe initialDelaySeconds |
| controllerManager.livenessProbe.periodSeconds | int | `20` | ControllerManager's livenessProbe periodSeconds |
| controllerManager.livenessProbe.successThreshold | int | `1` | ControllerManager's livenessProbe successThreshold |
| controllerManager.livenessProbe.timeoutSeconds | int | `1` | ControllerManager's livenessProbe timeoutSeconds |
| controllerManager.manager.containerSecurityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true}` | ControllerManager's container securityContext |
| controllerManager.manager.image.pullPolicy | string | `"Always"` | ControllerManager's image pullPolicy. This should be set to 'IfNotPresent' for released version |
| controllerManager.manager.image.repository | string | `"us-central1-docker.pkg.dev/k8s-staging-images/kueue/kueue"` | ControllerManager's image repository |
| controllerManager.manager.image.tag | string | `"main"` | ControllerManager's image tag |
| controllerManager.manager.podAnnotations | object | `{}` |  |
| controllerManager.manager.podSecurityContext | object | `{"runAsNonRoot":true,"seccompProfile":{"type":"RuntimeDefault"}}` | ControllerManager's pod securityContext |
| controllerManager.manager.priorityClassName | string | `nil` | ControllerManager's pod priorityClassName |
| controllerManager.manager.resources | object | `{"limits":{"cpu":"2","memory":"512Mi"},"requests":{"cpu":"500m","memory":"512Mi"}}` | ControllerManager's pod resources |
| controllerManager.nodeSelector | object | `{}` | ControllerManager's nodeSelector |
| controllerManager.podDisruptionBudget.enabled | bool | `false` | Enable PodDisruptionBudget |
| controllerManager.podDisruptionBudget.minAvailable | int | `1` | PodDisruptionBudget's topologySpreadConstraints |
| controllerManager.readinessProbe.failureThreshold | int | `3` | ControllerManager's readinessProbe failureThreshold |
| controllerManager.readinessProbe.initialDelaySeconds | int | `5` | ControllerManager's readinessProbe initialDelaySeconds |
| controllerManager.readinessProbe.periodSeconds | int | `10` | ControllerManager's readinessProbe periodSeconds |
| controllerManager.readinessProbe.successThreshold | int | `1` | ControllerManager's readinessProbe successThreshold |
| controllerManager.readinessProbe.timeoutSeconds | int | `1` | ControllerManager's readinessProbe timeoutSeconds |
| controllerManager.replicas | int | `1` | ControllerManager's replicas count |
| controllerManager.tolerations | list | `[]` | ControllerManager's tolerations |
| controllerManager.topologySpreadConstraints | list | `[]` | ControllerManager's topologySpreadConstraints |
| enableCertManager | bool | `false` | Enable x509 automated certificate management using cert-manager (cert-manager.io) |
| enableKueueViz | bool | `false` | Enable KueueViz dashboard |
| enablePrometheus | bool | `false` | Enable Prometheus |
| enableVisibilityAPF | bool | `false` | Enable API Priority and Fairness configuration for the visibility API |
| fullnameOverride | string | `""` | Override the resource name |
| kubernetesClusterDomain | string | `"cluster.local"` | Kubernetes cluster's domain |
| kueueViz.backend.image.pullPolicy | string | `"Always"` | KueueViz dashboard backend image pullPolicy. This should be set to 'IfNotPresent' for released version |
| kueueViz.backend.image.repository | string | `"us-central1-docker.pkg.dev/k8s-staging-images/kueue/kueueviz-backend"` | KueueViz dashboard backend image repository |
| kueueViz.backend.image.tag | string | `"main"` | KueueViz dashboard backend image tag |
| kueueViz.backend.imagePullSecrets | list | `[]` | Sets ImagePullSecrets for KueueViz dashboard backend deployments. This is useful when the images are in a private registry. |
| kueueViz.backend.ingress.host | string | `"backend.kueueviz.local"` | KueueViz dashboard backend ingress host |
| kueueViz.backend.ingress.ingressClassName | string | `nil` | KueueViz dashboard backend ingress class name |
| kueueViz.backend.ingress.tlsSecretName | string | `"kueueviz-backend-tls"` | KueueViz dashboard backend ingress tls secret name |
| kueueViz.backend.nodeSelector | object | `{}` | KueueViz backend nodeSelector |
| kueueViz.backend.priorityClassName | string | `nil` | Enable PriorityClass for KueueViz dashboard backend deployments |
| kueueViz.backend.tolerations | list | `[]` | KueueViz backend tolerations |
| kueueViz.frontend.image.pullPolicy | string | `"Always"` | KueueViz dashboard frontend image pullPolicy. This should be set to 'IfNotPresent' for released version |
| kueueViz.frontend.image.repository | string | `"us-central1-docker.pkg.dev/k8s-staging-images/kueue/kueueviz-frontend"` | KueueViz dashboard frontend image repository |
| kueueViz.frontend.image.tag | string | `"main"` | KueueViz dashboard frontend image tag |
| kueueViz.frontend.imagePullSecrets | list | `[]` | Sets ImagePullSecrets for KueueViz dashboard frontend deployments. This is useful when the images are in a private registry. |
| kueueViz.frontend.ingress.host | string | `"frontend.kueueviz.local"` | KueueViz dashboard frontend ingress host |
| kueueViz.frontend.ingress.ingressClassName | string | `nil` | KueueViz dashboard frontend ingress class name |
| kueueViz.frontend.ingress.tlsSecretName | string | `"kueueviz-frontend-tls"` | KueueViz dashboard frontend ingress tls secret name |
| kueueViz.frontend.nodeSelector | object | `{}` | KueueViz frontend nodeSelector |
| kueueViz.frontend.priorityClassName | string | `nil` | Enable PriorityClass for KueueViz dashboard frontend deployments |
| kueueViz.frontend.tolerations | list | `[]` | KueueViz frontend tolerations |
| managerConfig.controllerManagerConfigYaml | string | controllerManagerConfigYaml | controller_manager_config.yaml. ControllerManager utilizes this yaml via manager-config Configmap. |
| metrics.prometheusNamespace | string | `"monitoring"` | Prometheus namespace |
| metrics.serviceMonitor.tlsConfig | object | `{"insecureSkipVerify":true}` | ServiceMonitor's tlsConfig |
| metricsService.annotations | object | `{}` | metricsService's annotations |
| metricsService.labels | object | `{}` | metricsService's labels |
| metricsService.ports | list | `[{"name":"https","port":8443,"protocol":"TCP","targetPort":8443}]` | metricsService's ports |
| metricsService.type | string | `"ClusterIP"` | metricsService's type |
| mutatingWebhook.reinvocationPolicy | string | `"Never"` | MutatingWebhookConfiguration's reinvocationPolicy |
| nameOverride | string | `""` | Override the resource name |
| webhookService.ipDualStack.enabled | bool | `false` | webhookService's ipDualStack enabled |
| webhookService.ipDualStack.ipFamilies | list | `["IPv6","IPv4"]` | webhookService's ipDualStack ipFamilies |
| webhookService.ipDualStack.ipFamilyPolicy | string | `"PreferDualStack"` | webhookService's ipDualStack ipFamilyPolicy |
| webhookService.ports | list | `[{"port":443,"protocol":"TCP","targetPort":9443}]` | webhookService's ports |
| webhookService.type | string | `"ClusterIP"` | webhookService's type |
