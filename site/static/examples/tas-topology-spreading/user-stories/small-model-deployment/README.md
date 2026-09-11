# Story 1: small AI inference model, many replicas (Deployment)

From [KEP-13746](../../../../../../keps/13746-tas-topology-spreading/README.md),
"Serving a small AI inference model with multiple replicas".

The model fits in one Pod, so the service is a Deployment and Kueue creates one
Workload per Pod. A zone may already hold at most **45%** of those Workloads for
the next one to still be placed there.

## Fix applied vs. the KEP story

* **Added `kueue.x-k8s.io/podset-required-topology`.** The implementation counts
  a PodSet group as occupying exactly one domain per rule level, which only holds
  when TAS placement is required, so `validateTopologySpreadingAnnotation`
  rejects the spreading annotation without it. The story omits it. Here it names
  the same level as the rule (zone) — the natural choice for a single-Pod PodSet.

The story's explicit `workloadLabelSelectors` on the `app` label is kept exactly
as written: it is required, and for a Deployment it is the only thing that can
group the replicas, since each Pod is its own Workload with its own
`kueue.x-k8s.io/job-uid`.

## Run it

```sh
kind create cluster --config kind-cluster.yaml

# Merge the featureGates + labelKeysToCopy fragment into Kueue's config, then
# roll the controller so it takes effect.
kubectl -n kueue-system get cm kueue-manager-config -o jsonpath='{.data.controller_manager_config\.yaml}' > /tmp/kueue-config.yaml
# ... merge kueue-config-patch.yaml into /tmp/kueue-config.yaml ...
kubectl -n kueue-system create cm kueue-manager-config \
  --from-file=controller_manager_config.yaml=/tmp/kueue-config.yaml \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n kueue-system rollout restart deployment/kueue-controller-manager

kubectl apply -f sample-queues.yaml
kubectl apply -f deployment.yaml
```

## Verify

```sh
# One Workload per replica, each with a zone assignment.
kubectl -n story-small-model get workloads

# The zone each Pod actually landed in.
kubectl -n story-small-model get pods -o custom-columns=\
POD:.metadata.name,NODE:.spec.nodeName --no-headers |
  while read -r pod node; do
    echo "$pod $(kubectl get node "$node" -o jsonpath='{.metadata.labels.topology\.kubernetes\.io/zone}')"
  done | sort -k2
```

Expected: all three zones are opened before any zone takes a second replica
(`maxShareAllowingPlacement: "0.45"` needs `ceil(1/0.45) = 3` domains), and no
replica is ever placed into a zone that already holds more than 45% of the
admitted ones.

## Notes

* The gate is off by default, so without the config patch the annotation is
  ignored entirely — every replica schedules as if it were absent, and the
  webhook does not validate it either.
* Both the Topology level in `sample-queues.yaml` and the rule's `topologyKey`
  must name the same label. A `Required` rule whose level is missing from the
  flavor's Topology makes the flavor unusable rather than being skipped.
