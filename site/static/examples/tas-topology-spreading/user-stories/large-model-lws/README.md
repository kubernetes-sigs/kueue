# Story 2: large AI inference model, many replicas (LeaderWorkerSet)

From [KEP-13746](../../../../../../keps/13746-tas-topology-spreading/README.md),
"Serving a large AI inference model with multiple replicas".

The model needs several closely connected Pods, so one LWS group is one replica.
Each group lands in a single rack, and a rack may already hold at most **45%** of
the groups for the next one to be placed there.

## No changes vs. the KEP story

This story runs exactly as the KEP writes it — no `workloadLabelSelectors`, no
`app` label, and no `integrations.labelKeysToCopy`. It is the only one of the
four that does.

Omitting the selector is what asks for the default: the Workload mutating webhook
injects
`[{"key": "kueue.x-k8s.io/job-uid", "operator": "In", "values": ["<lws-uid>"]}]`
before the Workload is stored, so the effective selector is readable on the
object rather than inferred at scheduling time.

This works here and nowhere else among the four because of *whose* UID the label
carries. The LWS reconciler sets it to the **LeaderWorkerSet's** UID
(`leaderworkerset_reconciler.go:350`), so all six groups share one value and
spread against each other. In [../small-model-deployment/](../small-model-deployment/)
and [../soft-spreading-preferred/](../soft-spreading-preferred/) each Pod is its
own Workload labelled with its own UID, so the default would match only the group
being placed — those stories need an explicit selector and the
`labelKeysToCopy` entry to propagate the label it matches.

`cloud.provider.com/rack` is the KEP's too — this example ships its own
rack-labelled cluster and Topology.

## Why `podset-group-name` matters here

It fuses leader and worker into one spreading unit, so a replica counts **once**
rather than twice: `occupiedDomainsForGroup` unions the domains of every PodSet
sharing the key and increments `Total` a single time.

It must be the **same value for every replica** — which it is, since it comes
from the shared template. Contrast [../large-model-podgroup/](../large-model-podgroup/),
where the KEP gives each replica its *own* group name and thereby switches
spreading off silently.

## Requirements

The LWS integration must be enabled and the
[LeaderWorkerSet controller](https://github.com/kubernetes-sigs/lws) installed.

## Run it

```sh
kind create cluster --config kind-cluster.yaml
# install the LeaderWorkerSet controller, then Kueue

# Merge featureGates into Kueue's config and roll it.
kubectl -n kueue-system get cm kueue-manager-config -o jsonpath='{.data.controller_manager_config\.yaml}' > /tmp/kueue-config.yaml
# ... merge kueue-config-patch.yaml into /tmp/kueue-config.yaml ...
kubectl -n kueue-system create cm kueue-manager-config \
  --from-file=controller_manager_config.yaml=/tmp/kueue-config.yaml \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n kueue-system rollout restart deployment/kueue-controller-manager

kubectl apply -f sample-queues.yaml
kubectl apply -f leaderworkerset.yaml
```

## Verify

```sh
# One Workload per group, each with two PodSets in one rack.
kubectl -n story-large-lws get workloads

# The selector the webhook injected - the same job UID on every group, and equal
# to the LeaderWorkerSet's own UID.
kubectl -n story-large-lws get workloads -o jsonpath=\
'{range .items[*]}{.metadata.name}{"\t"}{.spec.podSets[0].template.metadata.annotations.kueue\.x-k8s\.io/podset-topology-spreading}{"\n"}{end}'
kubectl -n story-large-lws get lws large-inference-service -o jsonpath='{.metadata.uid}{"\n"}'

# The rack each group landed in - both Pods of a group must share it.
kubectl -n story-large-lws get pods -o custom-columns=\
POD:.metadata.name,NODE:.spec.nodeName --no-headers |
  while read -r pod node; do
    echo "$pod $(kubectl get node "$node" -o jsonpath='{.metadata.labels.cloud\.provider\.com/rack}')"
  done | sort -k2
```

Expected: leader and worker of each replica share a rack, all three racks are
opened before any rack takes a second group, and the six groups end up roughly
`2/2/2`.

## Notes

* The KEP story uses `replicas: 10` with `size: 4`. Scaled to `6`/`2` so the
  example runs on a three-node kind cluster; the spreading behaviour is the same.
* The rule's `topologyKey` may not name a level *below* the one requested by
  `podset-required-topology` — the group lands in a single domain at the
  requested level, so there is no choice left to make below it. Equal levels, as
  here, are fine.
