#!/bin/bash

# shellcheck disable=SC1091
if test -f .env; then
  source .env
fi

CL2_HOME_DIR=${CL2_HOME_DIR:=/Users/johny/perf-tests/clusterloader2} 
CL2_BINARY_NAME=${CL2_BINARY_NAME:=clusterloader}

USE_KUEUE=${USE_KUEUE:=true}

DEFAULT_EXPERIMENTS=(
    "10 2 0 2s 3m 100 100Gi"
)

EXPERIMENTS=("${EXPERIMENTS[@]:=${DEFAULT_EXPERIMENTS[@]}}")

export KUBECONFIG=${KUBECONFIG:="$HOME/.kube/config"}

PROVIDER=${PROVIDER:=gke}

export EXEC_DEPLOYMENT_YAML="$CL2_HOME_DIR/pkg/execservice/manifest/exec_deployment.yaml"

cp -r "$CL2_HOME_DIR/pkg/prometheus/manifests/" tmp_manifests
trap 'rm -r tmp_manifests' EXIT

PROMETHEUS_MANIFEST_PATH=$(pwd)/tmp_manifests
export PROMETHEUS_MANIFEST_PATH

now=$(date +%Y-%m-%d-%H.%M.%S)

if [[ "$USE_KUEUE" == true ]]; then
    export CL2_USE_KUEUE=true
    # Kustomize places all Kubernetes object manifests (role, rolebinding and servicemonitor) in the same file, 
    # however, Clusterloader expects, that there is one manifest per file, otherwise it does not create all the 
    # objects from the file. 
    # The yq expression below splits produced manifest into 3 files and then moves 
    # to temporary $PROMETHEUS_MANIFEST_PATH
    kubectl kustomize ../../config/prometheus | yq -s '.kind' -o yaml
    mv Role.yml "$PROMETHEUS_MANIFEST_PATH/prometheus-kueue-role.yaml"
    mv RoleBinding.yml "$PROMETHEUS_MANIFEST_PATH/prometheus-kueue-role-binding.yaml"
    mv ServiceMonitor.yml "$PROMETHEUS_MANIFEST_PATH/prometheus-kueue-service-monitor.yaml"
    kubectl apply -f prerequisites/resource-flavor.yaml
    report_dir_name="kueue_report_$now"
else 
    report_dir_name="report_$now"
fi
mkdir -p "$report_dir_name"

{
    echo -ne "Test Arguments,"
    echo -ne "P50 Job Create to start latency (ms),"
    echo -ne "P90 Job Create to start latency (ms),"
    echo -ne "P50 Job Start to complete latency (ms),"
    echo -ne "P90 Job Start to complete latency (ms),"
    echo -ne "Max Job Throughput (max jobs/s),"
    echo -ne "Total Jobs,"
    echo -ne "Total Pods,"
    echo -ne "Duration (s),"
    echo -ne "Avg Pod Waiting time (s),"
    echo -ne "P90 Pod Waiting time (s),"
    echo -ne "Avg Pod Completion time (s),"
    echo "P90 Pod Completion time (s)"
} >>"$report_dir_name/summary.csv"

for item in "${EXPERIMENTS[@]}"; do
    IFS=" " read -ra conditions <<<"$item"
    export CL2_SMALL_JOBS="${conditions[0]}"
    export CL2_MEDIUM_JOBS="${conditions[1]}"
    export CL2_LARGE_JOBS="${conditions[2]}"
    export CL2_JOB_RUNNING_TIME="${conditions[3]}"
    export CL2_TEST_TIMEOUT="${conditions[4]}"
    cores="${conditions[5]}"
    memory="${conditions[6]}"
    experiment_dir="$report_dir_name/$CL2_SMALL_JOBS-$CL2_MEDIUM_JOBS-$CL2_LARGE_JOBS-$CL2_JOB_RUNNING_TIME-$CL2_TEST_TIMEOUT-$cores-$memory"
    mkdir -p "$experiment_dir"
    echo "======================================================================================"
    echo "Running an experiment with [$CL2_SMALL_JOBS, $CL2_MEDIUM_JOBS, $CL2_LARGE_JOBS, $CL2_JOB_RUNNING_TIME, $CL2_TEST_TIMEOUT, $cores, $memory]"
    if [[ "$USE_KUEUE" == true ]]; then
        cp prerequisites/cluster-queue.template prerequisites/cluster-queue.yaml        
        yq -i e ".spec.resourceGroups[0].flavors[0].resources[0].nominalQuota=$cores" prerequisites/cluster-queue.yaml
        yq -i e ".spec.resourceGroups[0].flavors[0].resources[1].nominalQuota=\"$memory\"" prerequisites/cluster-queue.yaml
        kubectl apply -f prerequisites/cluster-queue.yaml
    fi
    "$CL2_HOME_DIR/$CL2_BINARY_NAME" \
        --testconfig=config.yaml \
        --enable-prometheus-server=true \
        --provider="$PROVIDER" \
        --v=2 --prometheus-scrape-metrics-server=true \
        --prometheus-scrape-kube-state-metrics=true \
        --report-dir="$experiment_dir"
    echo "Experiment finished. Extracting results from the report..."
    {
        echo -ne "$CL2_SMALL_JOBS $CL2_MEDIUM_JOBS $CL2_LARGE_JOBS $CL2_JOB_RUNNING_TIME $CL2_TEST_TIMEOUT $cores $memory,"
        echo -ne "$(jq '.dataItems[] | select(.labels.Metric=="create_to_start").data.Perc50' "$experiment_dir"/JobLifecycleLatency*.json),"
        echo -ne "$(jq '.dataItems[] | select(.labels.Metric=="create_to_start").data.Perc90' "$experiment_dir"/JobLifecycleLatency*.json),"
        echo -ne "$(jq '.dataItems[] | select(.labels.Metric=="start_to_complete").data.Perc50' "$experiment_dir"/JobLifecycleLatency*.json),"
        echo -ne "$(jq '.dataItems[] | select(.labels.Metric=="start_to_complete").data.Perc90' "$experiment_dir"/JobLifecycleLatency*.json),"
        echo -ne "$(jq '.dataItems[0].data.max_job_throughput' "$experiment_dir"/GenericPrometheusQuery*.json),"
        echo -ne "$(jq '.dataItems[0].data.total_jobs_scheduled' "$experiment_dir"/GenericPrometheusQuery*.json),"
        echo -ne "$(jq '.dataItems[0].data.total_pods_scheduled' "$experiment_dir"/GenericPrometheusQuery*.json),"
        echo -ne "$(jq '.dataItems[0].data.job_performance' "$experiment_dir"/Timer*.json)",
        echo -ne "$(jq '.dataItems[0].data.avg_pod_waiting_time' "$experiment_dir"/GenericPrometheusQuery*.json),"
        echo -ne "$(jq '.dataItems[0].data.perc_90_pod_waiting_time' "$experiment_dir"/GenericPrometheusQuery*.json),"
        echo -ne "$(jq '.dataItems[0].data.avg_pod_running_time' "$experiment_dir"/GenericPrometheusQuery*.json),"
        jq '.dataItems[0].data.perc_90_pod_completion_time' "$experiment_dir"/GenericPrometheusQuery*.json
    } >>"$report_dir_name/summary.csv"
    if [[ "$USE_KUEUE" == true ]]; then
        kubectl delete -f prerequisites/cluster-queue.yaml
    fi
done

if [[ "$USE_KUEUE" == true ]]; then
    kubectl delete -f prerequisites/resource-flavor.yaml
fi
