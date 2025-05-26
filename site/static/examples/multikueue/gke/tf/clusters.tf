provider "google" {
  project = var.project_id
}

# Create network and subnets for each region
resource "google_compute_network" "network" {
  name                    = "dws-network"
  auto_create_subnetworks = true

}

resource "google_container_cluster" "manager_cluster" {
  name     = "${var.cluster_manager_name_prefix}-${var.location_manager}"
  location = var.location_manager
  network  = google_compute_network.network.id
  remove_default_node_pool = true
  initial_node_count       = 1
  deletion_protection = false
  release_channel {
    channel = "RAPID"
  }

}

resource "google_container_node_pool" "manager_nodes" {
  name       = google_container_cluster.manager_cluster.name
  location   = var.location_manager
  cluster    = google_container_cluster.manager_cluster.name

  node_count = 0
  
  autoscaling {
    min_node_count = 0
    max_node_count = 1
  }

  node_config {
    machine_type = "n1-standard-4"

    guest_accelerator {
      type  = "nvidia-tesla-t4"
      count = 1
    }

    image_type = "COS_CONTAINERD"

    metadata = {
      "install-gpu-driver" = "true"
    }

    labels = {
      "cloud.google.com/gke-accelerator" = "nvidia-tesla-t4"
    }
  }

  # DWS
  queued_provisioning {
    enabled = false
  }
}

# Create GKE Autopilot worker clusters
resource "google_container_cluster" "worker_clusters" {
  for_each = {
    for region in var.regions_workers : region => {
      region = region
      name   = "${var.cluster_worker_names_prefix}-${region}" # Use prefix and region
    }
  }
  name                = each.value.name
  location            = each.value.region

  network             = google_compute_network.network.id # Reference the SINGLE network
  remove_default_node_pool = true
  initial_node_count       = 1
  deletion_protection = false
  release_channel {
    channel = "RAPID"
  }

}

resource "google_container_node_pool" "worker_nodes" {
  for_each = google_container_cluster.worker_clusters
  name       = each.value.name
  location   = each.value.location
  cluster    = each.value.name

  node_count = 0
  
  autoscaling {
    min_node_count = 0
    max_node_count = 1
  }

  node_config {
    machine_type = "n1-standard-4"

    guest_accelerator {
      type  = "nvidia-tesla-t4"
      count = 1
    }

    image_type = "COS_CONTAINERD"

    metadata = {
      "install-gpu-driver" = "true"
    }

    labels = {
      "cloud.google.com/gke-accelerator" = "nvidia-tesla-t4"
    }
  }

  # DWS
  queued_provisioning {
    enabled = false
  }
}

# Get the kubeconfig for each cluster and update the context
resource "null_resource" "update_kubeconfig" {
  for_each = google_container_cluster.worker_clusters
  triggers = {
    always_run    = timestamp()
    context_name  = each.value.name
  }
  provisioner "local-exec" {
    command = <<EOT
      gcloud container clusters get-credentials ${each.value.name} --region ${each.value.location} --project ${var.project_id}
      kubectl config rename-context gke_${var.project_id}_${each.value.location}_${each.value.name} ${each.value.name}

    EOT
  }

  provisioner "local-exec" {
    when    = destroy
    command = "kubectl config delete-context ${self.triggers.context_name} || true"
  }

  depends_on = [google_container_cluster.worker_clusters, google_container_node_pool.worker_nodes] # Ensure clusters are created first
}


# MultiKueue Manager Kubeconfig Update (Separate)
resource "null_resource" "update_manager_kubeconfig" {
  triggers = {
    always_run               = timestamp()
    context_name_to_destroy = "${var.cluster_manager_name_prefix}-${var.location_manager}"
  }

  provisioner "local-exec" {
    command = <<EOT
      gcloud container clusters get-credentials ${google_container_cluster.manager_cluster.name} --region ${google_container_cluster.manager_cluster.location} --project ${var.project_id}
      kubectl config rename-context gke_${var.project_id}_${google_container_cluster.manager_cluster.location}_${google_container_cluster.manager_cluster.name} ${google_container_cluster.manager_cluster.name}
    EOT
  }

  provisioner "local-exec" {
    when    = destroy
    command = "kubectl config delete-context ${self.triggers.context_name_to_destroy} || true"
  }

  depends_on = [google_container_cluster.manager_cluster, google_container_node_pool.manager_nodes]
}

