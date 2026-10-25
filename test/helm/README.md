# Helm upgrade tests

These tests run Helm's install and upgrade actions on `charts/kueue` and the
experimental Kueue Populator chart, using in-memory release storage and a fake
Kubernetes client. They check the values Helm saves for each release and the
rendered manager configuration across upgrades with and without
`--reuse-values`, `--reset-values` and `--reset-then-reuse-values`. Single
renders stay in the charts' `helm unittest` suites.

- `harness_test.go`: the Helm setup, chart builders and helpers.
- `kueue_test.go`: upgrade flags, migration from YAML, and `null` removal.
- `populator_test.go`: the Populator and wrapper chart defaults.

The tests are a separate Go module, so the Helm SDK's dependencies stay apart
from the repository's other modules. Keep `helm.sh/helm/v4` at the version in
`hack/tools/go.mod`.

Run them with the chart unit tests:

```sh
make helm-unit-test
```

Or on their own:

```sh
cd test/helm && go test ./...
```
