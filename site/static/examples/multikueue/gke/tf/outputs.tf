output "cluster_names_and_regions" {
  value = {
    for k, cluster in google_container_cluster.worker_clusters : k => {
      name    = cluster.name
      region  = cluster.location
      network = cluster.network
      subnet  = cluster.subnetwork
    }
  }
}
