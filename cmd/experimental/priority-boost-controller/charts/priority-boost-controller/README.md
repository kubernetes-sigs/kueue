# priority-boost-controller

Helm chart for deploying the experimental priority-boost controller.

## Install

```bash
helm install priority-boost-controller ./charts/priority-boost-controller \
  --namespace kueue-system \
  --create-namespace
```

## Values

Main values are under `priorityBoostController`:

- `image.repository`
- `image.tag`
- `image.pullPolicy`
- `config.maxBoost`
