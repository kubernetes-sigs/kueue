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

The policies are deny-by-default: any port not listed below is unreachable.

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
depends on your cluster. To restrict a port to specific peers, add rules through
`networkPolicy.extraIngress`, as shown below.

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

## When to enable these policies

Enable them when your cluster runs a CNI plugin that enforces NetworkPolicy and you want
to reduce what a compromised or untrusted workload can reach. Kueue is a privileged
controller with broad watch permissions that also sits in the admission path, so limiting
its reachable surface is worthwhile on shared or multi-tenant clusters.

There are cases where turning them on needs more thought:

- **You run the ControllerManager with `hostNetwork: true`.** The policies cannot help
  here at all; see [Limitations](#limitations).
- **You use MultiKueue and want egress restriction too.** Egress is a separate opt-in for
  this reason; see [Restricting egress](#restricting-egress).
- **You have changed Kueue's ports** through `managerConfig`, for example by setting a
  different `metrics.bindAddress`. The policies allow the default ports, so adjust them
  with `networkPolicy.extraIngress` to match your configuration.
- **Your CNI plugin does not enforce NetworkPolicy.** The objects will be created and
  accepted by the API server but have no effect, which can give a false sense of
  protection.

### Why they are not enabled by default

The ControllerManager's admission webhook is reached by the kube-apiserver, which on most
distributions runs on the host network. Its traffic therefore arrives with a node IP and
matches no pod or namespace selector, and the correct `ipBlock` differs from cluster to
cluster, so the chart cannot know it.

If that rule were wrong on your cluster, the webhook would become unreachable and every
Job creation in the cluster would start failing. Making the policies opt-in means an
upgrade never changes the behaviour of an existing installation: you turn them on
deliberately, and you know to look at them if something stops working.

This is also why the rules above are scoped by port rather than by peer.

## Enabling the policies

### Helm

The policies are disabled by default. Enable them with:

```yaml
networkPolicy:
  enabled: true
```

```bash
helm upgrade --install kueue oci://registry.k8s.io/kueue/charts/kueue \
  --namespace kueue-system \
  --create-namespace \
  -f values.yaml
```

### Kustomize

Apply the `networkpolicy` overlay alongside your existing installation:

```bash
kubectl apply -k config/networkpolicy
```

## Restricting a port to specific peers

`networkPolicy.extraIngress` appends rules to the ControllerManager's policy. For
example, to additionally allow the metrics port only from a `monitoring` namespace:

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
          port: 8443
```

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

- **hostNetwork is not supported.** Most CNI plugins do not apply NetworkPolicy to pods
  on the host network, so the policy would have no effect. Setting both
  `networkPolicy.enabled` and `controllerManager.hostNetwork` fails the Helm render
  rather than giving a false sense of protection. Restrict a host-network deployment at
  the node firewall instead.
- The KueueViz policies are inert unless the KueueViz components are also deployed.
