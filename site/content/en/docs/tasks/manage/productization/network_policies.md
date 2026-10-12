---
title: "Restrict network traffic with NetworkPolicies"
date: 2026-08-10
weight: 3
description: >
  Restrict which traffic can reach the Kueue components
---

This page shows how to restrict network traffic to the Kueue components using
[Kubernetes NetworkPolicies](https://kubernetes.io/docs/concepts/services-networking/network-policies/),
addressing the OWASP Kubernetes Top Ten entry
_K05: Missing Network Segmentation Controls_.

The page is intended for a [batch administrator](/docs/tasks#batch-administrator).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation).
- Your cluster uses a CNI plugin that **enforces** NetworkPolicy, such as Calico or
  Cilium. NetworkPolicy objects are accepted by every cluster, but a plugin that does
  not implement them, including the default CNI used by `kind`, silently ignores them.

## What the policies allow

Selecting a pod with a NetworkPolicy isolates it, so ingress is denied unless a rule
allows it. The rules below therefore leave every port not listed unreachable, while each
listed port stays reachable from any source.

Two limits are worth knowing. Enforcement is entirely up to your CNI plugin, and
Kubernetes allows traffic whose source is the pod's own node regardless of any policy, so
these rules cannot isolate a port from something running on the same node. Use node-level
controls if you need that.

| Component | Port | Purpose | Peer |
| --------- | ---- | ------- | ---- |
| ControllerManager | 9443 | Admission webhooks | kube-apiserver |
| ControllerManager | 8082 | Aggregated visibility API | kube-apiserver |
| ControllerManager | 8443 | Metrics | Prometheus |
| ControllerManager | 8081 | Health and readiness probes | kubelet |
| KueueViz backend | 8080 | Dashboard API and websocket | Ingress controller |
| KueueViz frontend | 8080 | Dashboard assets | Ingress controller |

These rules are scoped by port rather than by peer. The kube-apiserver and the kubelet
normally run on the host network, so their traffic cannot be matched by a pod or
namespace selector, and the address of the Prometheus scraper or the ingress controller
depends on your cluster. If you need a port restricted to particular peers as well, see
[Restricting a port to specific peers](#restricting-a-port-to-specific-peers).

### Ports that are blocked

One port Kueue can serve is deliberately not allowed: the ControllerManager's pprof
endpoint. It is disabled by default and only listens when `pprofBindAddress` is set in
the ControllerManager configuration. If you
[enable pprof](/docs/tasks/dev/enabling_pprof_endpoints), reach it with
`kubectl port-forward` rather than by opening the port to the cluster, since profiling
data may be sensitive.

The KueueViz backend also starts a pprof listener on `localhost:6060` when it runs
outside release mode, but that is bound to loopback and is not reachable from other pods
either way.

Apart from those, Kueue serves no port that these policies block.

## Default ingress policies

Ingress policies are enabled by default in Helm and included in the default Kustomize
build. They take effect when your cluster enforces NetworkPolicy. Kueue is a privileged
controller with broad watch permissions that also sits in the admission path, so limiting
its reachable surface is worthwhile on shared or multi-tenant clusters.

Check these settings when customizing an installation:

- **You run the ControllerManager with `hostNetwork: true`.** Helm skips its policies
  because enforcement depends on the CNI plugin; see [Limitations](#limitations).
- **You use MultiKueue and want egress restriction too.** Egress is a separate opt-in for
  this reason; see [Restricting egress](#restricting-egress).
- **You have changed Kueue's ports** through `managerConfig`, for example by setting a
  different `metrics.bindAddress`. Helm derives allowed ports from that configuration.
  With Kustomize, update the policy ports alongside your configuration.
- **Your CNI plugin does not enforce NetworkPolicy.** The objects will be created and
  accepted by the API server but have no effect, which can give a false sense of
  protection.

## Configuring the policies

### Helm

Ingress policies are enabled by default. To disable them, set:

```bash
helm upgrade --install kueue oci://registry.k8s.io/kueue/charts/kueue \
  --namespace kueue-system \
  --create-namespace \
  --set networkPolicy.enabled=false
```

Or set the same value in your own values file:

```yaml
networkPolicy:
  enabled: false
```

### Kustomize

The default build includes ingress policies. For an installation using a different
build, apply the standalone `networkpolicy` overlay from a checkout of the Kueue release
you are running:

```bash
kubectl apply -k config/networkpolicy
```

The overlay applies the ingress policies only. To restrict egress as well, uncomment
`manager-egress.yaml` in `config/components/networkpolicy/kustomization.yaml` first, and
read [Restricting egress](#restricting-egress) before you do.

## Allowing extra traffic

`networkPolicy.extraIngress` appends rules to the ControllerManager's policy. Use it to
permit traffic the built-in rules do not cover, for example a scraper that needs a port
which is not in the table above.

```yaml
networkPolicy:
  enabled: true
  extraIngress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: monitoring
      ports:
        - protocol: TCP
          port: 9090
```

{{% alert title="Note" color="primary" %}}
Ingress rules within a NetworkPolicy are additive: traffic is allowed if **any** rule
matches it. `extraIngress` can therefore only widen what is permitted. Adding a rule that
names a peer for a port already in the table does not restrict that port, because the
built-in rule continues to allow it from every source.
{{% /alert %}}

## Restricting a port to specific peers

The chart does not expose the built-in rules for editing, so narrowing one means
replacing it. With Kustomize, write a patch and apply it over the overlay from your own
kustomization:

```yaml
# kustomization.yaml
resources:
  - <your Kueue checkout>/config/networkpolicy
patches:
  - path: metrics-peers-patch.yaml
```

```yaml
# metrics-peers-patch.yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: kueue-controller-manager-ingress
  namespace: kueue-system
spec:
  ingress:
    - ports:
        - protocol: TCP
          port: 9443
        - protocol: TCP
          port: 8082
    - ports:
        - protocol: TCP
          port: 8081
    # metrics, now limited to the monitoring namespace
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: monitoring
      ports:
        - protocol: TCP
          port: 8443
```

Because a patch on `spec.ingress` replaces the whole list, every rule you still want has
to be repeated. With Helm, apply the same replacement through a
[post-renderer](https://helm.sh/docs/topics/advanced/#post-rendering).

Take care when narrowing 9443 and 8082. Those are reached by the kube-apiserver, which
usually runs on the host network, so a pod or namespace selector will not match it and
you need an `ipBlock` covering your control-plane node addresses. Getting it wrong makes
the admission webhook unreachable and stops Job creation across the cluster. 8443 is the
safer one to narrow, since a mistake only breaks metrics scraping.

## Restricting egress

Egress restriction is a separate opt-in, because it interacts with MultiKueue:

```yaml
networkPolicy:
  enabled: true
  egress:
    enabled: true
```

This allows DNS, and the API server ports 443 and 6443 to any address. The wide address
range exists because [MultiKueue](/docs/concepts/multikueue) connects to remote cluster
API servers whose addresses come from Secrets and ClusterProfiles at runtime and cannot
be known in advance.

If you do not use MultiKueue, or your remote clusters sit in a known range, narrow this
with `networkPolicy.egress.extraEgress` and remove the broad rule with a
[post-renderer](https://helm.sh/docs/topics/advanced/#post-rendering) or a Kustomize
patch.

{{% alert title="Warning" color="warning" %}}
Enabling egress applies deny-by-default egress to the ControllerManager. If a MultiKueue
remote cluster serves its API on a port other than 443 or 6443, the connection is
dropped and its `MultiKueueCluster` becomes inactive. Add the port to
`networkPolicy.egress.extraEgress` before enabling.
{{% /alert %}}

## Limitations

- **hostNetwork enforcement depends on the CNI plugin.** Kubernetes leaves the behaviour of NetworkPolicy for
  pods on the host network undefined, and most CNI plugins do not apply policies to them,
  so the policy would likely have no effect. Helm skips the ControllerManager ingress
  and egress policies when `controllerManager.hostNetwork` is enabled. Kustomize users
  should remove those policies when enabling host networking. Use node firewall rules
  where your CNI does not enforce policies for host-network pods.
- The KueueViz policies are inert unless the KueueViz components are also deployed.
