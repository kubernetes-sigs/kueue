provider "google" {
  project = var.project_id
}

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
}

resource "google_container_node_pool" "default_nodepool" {
  name       = "default-nodepool"
  location   = var.location_manager
  cluster    = google_container_cluster.manager_cluster.name
  

  initial_node_count = 1

  node_config {
    machine_type = "e2-standard-2"
    image_type   = "UBUNTU_CONTAINERD"
  }
}

resource "google_container_node_pool" "manager_nodes" {
  name       = "dws-nodepool"
  location   = var.location_manager
  cluster    = google_container_cluster.manager_cluster.name

  node_count = 0
  
  autoscaling {
    min_node_count = 0
    max_node_count = 3
  }

  management {
    auto_repair = false
  }

  node_config {
    machine_type = "n1-standard-4"

    guest_accelerator {
      type  = "nvidia-tesla-t4"
      count = 1
      gpu_driver_installation_config {
        gpu_driver_version = "DEFAULT"
      }
    }

    reservation_affinity {
      consume_reservation_type = "NO_RESERVATION"
    }
  }

  # DWS
  queued_provisioning {
    enabled = true
  }
}


resource "google_container_cluster" "worker_cluster" {
  name     = "${var.cluster_worker_names_prefix}-${var.region_worker}"
  location = var.region_worker
  network  = google_compute_network.network.id
  remove_default_node_pool = true
  initial_node_count       = 1
  deletion_protection = false
}

resource "google_container_node_pool" "worker_default_nodepool" {
  name       = "${google_container_cluster.worker_cluster.name}-default-nodepool"
  location   = var.region_worker
  cluster    = google_container_cluster.worker_cluster.name

  initial_node_count = 1

  node_config {
    machine_type = "e2-standard-2"
    image_type   = "UBUNTU_CONTAINERD"
  }
}

resource "google_container_node_pool" "worker_nodes" {
  name       = "${google_container_cluster.worker_cluster.name}-dws-nodepool"
  location   = var.region_worker
  cluster    = google_container_cluster.worker_cluster.name

  node_count = 0
  
  autoscaling {
    min_node_count = 0
    max_node_count = 3
  }

  management {
    auto_repair = false
  }

  node_config {
    machine_type = "n1-standard-4"

    guest_accelerator {
      type  = "nvidia-tesla-t4"
      count = 1
      gpu_driver_installation_config {
        gpu_driver_version = "DEFAULT"
      }
    }

    reservation_affinity {
      consume_reservation_type = "NO_RESERVATION"
    }
  }

  # DWS
  queued_provisioning {
    enabled = true
  }
}


resource "null_resource" "update_kubeconfig" {
  triggers = {
    always_run    = timestamp()
    context_name  = google_container_cluster.worker_cluster.name
  }
  provisioner "local-exec" {
    command = <<EOT
      gcloud container clusters get-credentials ${google_container_cluster.worker_cluster.name} --region ${var.region_worker} --project ${var.project_id}
      kubectl config rename-context gke_${var.project_id}_${var.region_worker}_${google_container_cluster.worker_cluster.name} ${google_container_cluster.worker_cluster.name}

    EOT
  }

  provisioner "local-exec" {
    when    = destroy
    command = "kubectl config delete-context ${self.triggers.context_name} || true"
  }

  depends_on = [google_container_cluster.worker_cluster, google_container_node_pool.worker_nodes, google_container_node_pool.worker_default_nodepool] # Ensure clusters are created first
}


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

  depends_on = [google_container_cluster.manager_cluster, google_container_node_pool.manager_nodes, google_container_node_pool.default_nodepool]
}

