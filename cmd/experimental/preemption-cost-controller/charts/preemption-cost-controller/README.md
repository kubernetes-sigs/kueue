# preemption-cost-controller

Helm chart for deploying the experimental preemption-cost controller.

## Install

```bash
helm install preemption-cost-controller ./charts/preemption-cost-controller \
  --namespace kueue-system \
  --create-namespace
```

## Values

Main values are under `preemptionCostController`:

- `image.repository`
- `image.tag`
- `image.pullPolicy`
- `config.enableRunningPhaseIncrement`
- `config.runningPhaseIncrement`
- `config.maxCost`
