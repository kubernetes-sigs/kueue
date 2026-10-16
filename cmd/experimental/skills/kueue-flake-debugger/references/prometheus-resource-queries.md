<!--
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Prow Prometheus resource queries

Run this step only when `build-logs/podinfo.json` is available. The Grafana UI at
`https://monitoring-eks.prow.k8s.io` may require login. The Prow datasource
proxy API can expose the required public metrics without using the UI. Discover the Prometheus
datasource UID from the public frontend settings instead of hardcoding it. If multiple Prometheus
datasources exist, inspect the settings and select the intended UID before querying. Do not bypass
authentication if the endpoint changes or becomes private.

Set the Pod name from `podinfo.json` and use a time range covering the test container lifetime:

```sh
set -e
GRAFANA='https://monitoring-eks.prow.k8s.io'
curl -fsS "${GRAFANA}/api/frontend/settings" -o build-logs/grafana-settings.json
DS_UID=$(jq -er '[.datasources[] | select(.type == "prometheus") | .uid] |
  if length == 1 then .[0] else error("Select the intended Prometheus datasource UID.") end' \
  build-logs/grafana-settings.json)
PROM_API="${GRAFANA}/api/datasources/proxy/uid/${DS_UID}/api/v1"
POD=$(jq -r '.pod.metadata.name' build-logs/podinfo.json)
START=$(jq -r '.pod.status.containerStatuses[] | select(.name == "test") | .state.terminated.startedAt | fromdateiso8601' build-logs/podinfo.json)
END=$(jq -r '.pod.status.containerStatuses[] | select(.name == "test") | .state.terminated.finishedAt | fromdateiso8601' build-logs/podinfo.json)

curl -fsS --get "${PROM_API}/query_range" \
  --data-urlencode "query=container_memory_working_set_bytes{namespace=\"test-pods\",pod=\"${POD}\",container=\"test\"}" \
  --data-urlencode "start=${START}" \
  --data-urlencode "end=${END}" \
  --data-urlencode 'step=30' \
  -o build-logs/memory.json

curl -fsS --get "${PROM_API}/query_range" \
  --data-urlencode "query=container_oom_events_total{namespace=\"test-pods\",pod=\"${POD}\",container=\"test\"}" \
  --data-urlencode "start=${START}" \
  --data-urlencode "end=${END}" \
  --data-urlencode 'step=30' \
  -o build-logs/oom.json

curl -fsS --get "${PROM_API}/query_range" \
  --data-urlencode "query=rate(container_cpu_usage_seconds_total{namespace=\"test-pods\",pod=\"${POD}\",container=\"test\"}[2m])" \
  --data-urlencode "start=${START}" \
  --data-urlencode "end=${END}" \
  --data-urlencode 'step=30' \
  -o build-logs/cpu.json

curl -fsS --get "${PROM_API}/query_range" \
  --data-urlencode "query=100 * rate(container_cpu_cfs_throttled_periods_total{namespace=\"test-pods\",pod=\"${POD}\",container=\"test\"}[2m]) / (rate(container_cpu_cfs_periods_total{namespace=\"test-pods\",pod=\"${POD}\",container=\"test\"}[2m]) > 0)" \
  --data-urlencode "start=${START}" \
  --data-urlencode "end=${END}" \
  --data-urlencode 'step=30' \
  -o build-logs/cpu-throttling.json
```

Extract the relevant values:

```sh
jq '[.data.result[]?.values[]?[1] | tonumber] | {max:max}' build-logs/memory.json
jq '[.data.result[]?.values[]?[1] | tonumber] | {max:max}' build-logs/oom.json
jq '[.data.result[]?.values[]?[1] | tonumber] | {max:max}' build-logs/cpu.json
jq '[.data.result[]?.values[]?[1] | tonumber] | {max:max}' build-logs/cpu-throttling.json
```

Record the units explicitly. For example, `12,871,102,464` bytes is about `11.987 GiB`, while a
`12 GiB` limit is `12,884,901,888` bytes. A result at 99% of the limit supports a resource-pressure
hypothesis but does not establish an OOM kill.

CPU usage is measured in cores; compare it with the `test` container CPU limit. The CFS ratio is
the percentage of periods with throttling, not the percentage of CPU time lost. Near-limit CPU
usage with a high ratio supports CPU quota pressure but does not prove the failure's cause.
The CFS query excludes zero-period windows. For any metric, a null maximum means samples are
missing; report an evidence gap instead of treating it as zero usage or no OOM events.
