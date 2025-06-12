# Pending Workload Dashboard

### How to generate a Service Account Token

```shell
TOKEN=$(kubectl create token default -n default)
echo $TOKEN
```

## How to Create an Infinity data source.

1. Go to `Connections` >` Data sources` and click `+ Add new data source`.
2. Select `Infinity`.
3. Configure the data source:
    - Authentication: Set the `Bearer Token` generated in previous step.
    - Network: Enable `Skip TLS Verify`.
    - Security: Add `https://kubernetes.default.svc` to allowed hosts and set `Query security` to `Allowed`.
4. Click `Save & test` to verify the configuration.

## How to Create a Pending Workloads for ClusterQueue Visibility Dashboard from Scratch

1. In `Grafana`, go to `Dashboards` > `New` > `New dashboard`.
2. Click `Settings`.
3. Enter `Pending Workloads for ClusterQueue visibility` in the `Title` field.
4. Click `Variables` > `+ New variable`.
   - Set `Variable Type` to `Query`.
   - Enter `cluster_queue` in the `Name` field.
   - Enter `Cluster Queue` in the `Label` field.
   - Set `Hide` to `Nothing`.
   - Under `Query Options`, select `Data source` as `yesoreyeram-infinity-datasource`.
   - Set `Query Type` to `Infinity`.
   - Enter the URL: https://kubernetes.default.svc/apis/kueue.x-k8s.io/v1beta1/clusterqueues.
   - Expand `Parsing Options` and set `Rows Root` to `items`.
   - In `Rows Root` put `items`.
   - Click `Add Columns` and add a column with:
     - Selector: `metadata.name`, Title: `Name`, Format: `String`
   - Click `Run Query` to verify it lists all available `ClusterQueues`.
   - Click `Back to Dashboard`.
5. Click `Add` > `Visualization`.
6. Choose `yesoreyeram-infinity-datasource` as the `Data Source`.
7. Enter the URL: https://kubernetes.default.svc/apis/visibility.kueue.x-k8s.io/v1beta1/clusterqueues/$cluster_queue/pendingworkloads.
8. Expand `Parsing Options` and set `Rows Root` to `items`.
9. In `Rows Root` put `items`. 
10. Click `Add Columns` and add a columns with:
    - Selector: `metadata.name`, Title: `Name`, Format: `String`
    - Selector: `metadata.namespace`, Title: `Namespace`, Format: `String`
    - Selector: `metadata.creationTimestamp`, Title: `Creation Timestamp`, Format: `Timestamp`
    - Selector: `metadata.ownerReferences.0.name`, Title: `Owner Job`, Format: `String`
    - Selector: `priority`, Title: `Priority`, Format: `Number`
    - Selector: `localQueueName`, Title: `Local Queue`, Format: `String`
    - Selector: `positionInClusterQueue`, Title: `Cluster Queue Position`, Format: `Number`
    - Selector: `positionInLocalQueue`, Title: `Local Queue Position`, Format: `Number`
11. Choose `Table` as the `Visualization`.
12. Click `Save dashboard` > `Save`.

## How to Create a Pending Workloads for LocalQueue Visibility Dashboard from Scratch

1. In `Grafana`, go to `Dashboards` > `New` > `New Dashboard`.
2. Click `Settings`.
3. Enter `Pending Workloads for LocalQueue visibility` in the `Title` field.
4. Click `Variables` > `+ New variable`.
    - Set `Variable Type` to `Query`.
    - Enter `namespace` in the `Name` field.
    - Enter `Namespace` in the `Label` field.
    - Set `Hide` to `Nothing`.
    - Under `Query Options`, select `Data Source` as `yesoreyeram-infinity-datasource`.
    - Set `Query Type` to `Infinity`.
    - Enter the URL: https://kubernetes.default.svc/api/v1/namespaces.
    - Expand `Parsing Options` and set `Rows Root` to `items`.
    - Click `Add Columns` and add a column with:
        - Selector: `metadata.name`
        - Title: `Name`
    - Click `Run Query` to verify it lists all available `LocalQueues`.
    - Click `Back to List`.
5. Click `Back to List` > `+ New variable`.
   - Set `Variable Type` to `Query`. 
   - Enter `local_queue` in the `Name` field. 
   - Enter `Local Queue` in the `Label` field. 
   - Set `Hide` to `Nothing`. 
   - Under `Query Options`, select `Data Source` as `yesoreyeram-infinity-datasource`. 
   - Set `Query Type` to `Infinity`. 
   - Enter the URL: https://kubernetes.default.svc/apis/visibility.kueue.x-k8s.io/v1beta1/clusterqueues/$cluster_queue/pendingworkloads. 
   - Expand `Parsing Options` and set `Rows Root` to `items`. 
   - Click `Add Columns` and add a column with:
     - Selector: `metadata.name`
     - Title: `Name`
   - Click `Run Query` to verify it lists all available `LocalQueues`.
   - Click `Back to Dashboard`.
6. Click `Add` > `Visualization`. 
7. Select `yesoreyeram-infinity-datasource` as the `Data Source`. 
8. Enter the URL: https://kubernetes.default.svc/apis/visibility.kueue.x-k8s.io/v1beta1/namespaces/${namespace}/localqueues/${local_queue}/pendingworkloads.
9. Expand `Parsing Options` and set `Rows Root` to `items`.
10. In `Rows Root` put `items`.
11. Click `Add Columns` and add a columns with:
    - Selector: `metadata.name`, Title: `Name`, Format: `String`
    - Selector: `metadata.namespace`, Title: `Namespace`, Format: `String`
    - Selector: `metadata.creationTimestamp`, Title: `Creation Timestamp`, Format: `Timestamp`
    - Selector: `metadata.ownerReferences.0.name`, Title: `Owner Job`, Format: `String`
    - Selector: `priority`, Title: `Priority`, Format: `Number`
    - Selector: `localQueueName`, Title: `Local Queue`, Format: `String`
    - Selector: `positionInClusterQueue`, Title: `Cluster Queue Position`, Format: `Number`
    - Selector: `positionInLocalQueue`, Title: `Local Queue Position`, Format: `Number`
12. Choose `Table` as the `Visualization`.
13. Click `Save dashboard` > `Save`.

## How to Export a Dashboard in Grafana

1. In `Grafana`, go to `Dashboards` and select the desired dashboard.
2. Click the `Export` in the top-right corner.
3. Select the `Export as JSON` tab.
4. Enable the option `Export for sharing externally` (if you want the dashboard to be used in another Grafana instance).
5. Click `Download file` to download the JSON file and choose a save location.