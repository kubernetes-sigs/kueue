terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
    }
  }
}

# Variables
variable "project_id" {
  type        = string
  description = "GCP project ID"
}

variable "location_manager" {
  type        = string
  description = "Location of GKE manager cluster"
  default     = "europe-west4-a"
}

variable "cluster_manager_name_prefix" {
  type        = string
  description = "Prefix of MultiKueue Manager GKE cluster"
  default     = "manager"
}

variable "region_worker" {
  type        = string
  description = "Region of first worker cluster"
  default     = "us-east4-a"
}

variable "region_worker_2" {
  type        = string
  description = "Region of second worker cluster"
  default     = "asia-southeast1-a"
}

variable "cluster_worker_names_prefix" {
  type        = string
  description = "Prefix of MultiKueue Workers GKE cluster"
  default     = "worker"
}

variable "enable_dws" {
  type        = bool
  description = "Enable DWS (Dynamic Workload Scheduler) features"
  default     = false
}

# Provider configuration
provider "google" {
  project = var.project_id
}

# VPC Network
resource "google_compute_network" "network" {
  name                    = "test-network"
  auto_create_subnetworks = true
}

# Manager Cluster
resource "google_container_cluster" "manager_cluster" {
  name                     = "${var.cluster_manager_name_prefix}-${var.location_manager}"
  location                 = var.location_manager
  network                  = google_compute_network.network.id
  remove_default_node_pool = true
  initial_node_count       = 1
  deletion_protection      = false
}

resource "google_container_node_pool" "manager_default_nodepool" {
  name       = "${var.cluster_manager_name_prefix}-default-nodepool"
  location   = var.location_manager
  cluster    = google_container_cluster.manager_cluster.name
  initial_node_count = 1

  node_config {
    machine_type = "e2-standard-2"
    image_type   = "UBUNTU_CONTAINERD"
  }
}

# First Worker Cluster
resource "google_container_cluster" "worker_cluster" {
  name                     = "${var.cluster_worker_names_prefix}-${var.region_worker}"
  location                 = var.region_worker
  network                  = google_compute_network.network.id
  remove_default_node_pool = true
  initial_node_count       = 1
  deletion_protection      = false
}

resource "google_container_node_pool" "worker_default_nodepool" {
  name       = "${var.cluster_worker_names_prefix}-default-nodepool"
  location   = var.region_worker
  cluster    = google_container_cluster.worker_cluster.name
  initial_node_count = 1

  node_config {
    machine_type = "e2-standard-2"
    image_type   = "UBUNTU_CONTAINERD"
  }
}

resource "google_container_node_pool" "worker_nodepool" {
  name       = "${var.cluster_worker_names_prefix}-nodepool"
  location   = var.region_worker
  cluster    = google_container_cluster.worker_cluster.name
  node_count = var.enable_dws ? 0 : 1
  
  autoscaling {
    min_node_count = var.enable_dws ? 0 : 1
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

  # DWS - only enable when var.enable_dws is true
  queued_provisioning {
    enabled = var.enable_dws
  }
}

# Second Worker Cluster
resource "google_container_cluster" "worker_cluster_2" {
  name                     = "${var.cluster_worker_names_prefix}-${var.region_worker_2}"
  location                 = var.region_worker_2
  network                  = google_compute_network.network.id
  remove_default_node_pool = true
  initial_node_count       = 1
  deletion_protection      = false
}

resource "google_container_node_pool" "worker_default_nodepool_2" {
  name       = "${var.cluster_worker_names_prefix}-default-nodepool"
  location   = var.region_worker_2
  cluster    = google_container_cluster.worker_cluster_2.name
  initial_node_count = 1

  node_config {
    machine_type = "e2-standard-2"
    image_type   = "UBUNTU_CONTAINERD"
  }
}

resource "google_container_node_pool" "worker_nodepool_2" {
  name       = "${var.cluster_worker_names_prefix}-nodepool"
  location   = var.region_worker_2
  cluster    = google_container_cluster.worker_cluster_2.name
  node_count = var.enable_dws ? 0 : 1
  
  autoscaling {
    min_node_count = var.enable_dws ? 0 : 1
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

  # DWS - only enable when var.enable_dws is true
  queued_provisioning {
    enabled = var.enable_dws
  }
}

# Kubeconfig management for Manager Cluster
resource "null_resource" "update_manager_kubeconfig" {
  triggers = {
    always_run               = timestamp()
    context_name            = "${var.cluster_manager_name_prefix}-${var.location_manager}"
    original_context_name   = "gke_${var.project_id}_${var.location_manager}_${var.cluster_manager_name_prefix}-${var.location_manager}"
  }

  provisioner "local-exec" {
    command = <<EOT
      gcloud container clusters get-credentials ${google_container_cluster.manager_cluster.name} --region ${google_container_cluster.manager_cluster.location} --project ${var.project_id}
      kubectl config rename-context gke_${var.project_id}_${google_container_cluster.manager_cluster.location}_${google_container_cluster.manager_cluster.name} ${google_container_cluster.manager_cluster.name}
    EOT
  }

  depends_on = [google_container_cluster.manager_cluster, google_container_node_pool.manager_default_nodepool]
}

# Kubeconfig management for First Worker Cluster
resource "null_resource" "update_kubeconfig" {
  triggers = {
    always_run         = timestamp()
    context_name       = google_container_cluster.worker_cluster.name
    original_context_name = "gke_${var.project_id}_${var.region_worker}_${google_container_cluster.worker_cluster.name}"
  }
  
  provisioner "local-exec" {
    command = <<EOT
      gcloud container clusters get-credentials ${google_container_cluster.worker_cluster.name} --region ${var.region_worker} --project ${var.project_id}
      kubectl config rename-context gke_${var.project_id}_${var.region_worker}_${google_container_cluster.worker_cluster.name} ${google_container_cluster.worker_cluster.name}
    EOT
  }

  depends_on = [google_container_cluster.worker_cluster, google_container_node_pool.worker_nodepool, google_container_node_pool.worker_default_nodepool]
}

# Kubeconfig management for Second Worker Cluster
resource "null_resource" "update_worker2_kubeconfig" {
  triggers = {
    always_run         = timestamp()
    context_name       = google_container_cluster.worker_cluster_2.name
    original_context_name = "gke_${var.project_id}_${var.region_worker_2}_${google_container_cluster.worker_cluster_2.name}"
  }
  
  provisioner "local-exec" {
    command = <<EOT
      gcloud container clusters get-credentials ${google_container_cluster.worker_cluster_2.name} --region ${var.region_worker_2} --project ${var.project_id}
      kubectl config rename-context gke_${var.project_id}_${var.region_worker_2}_${google_container_cluster.worker_cluster_2.name} ${google_container_cluster.worker_cluster_2.name}
    EOT
  }

  depends_on = [google_container_cluster.worker_cluster_2, google_container_node_pool.worker_nodepool_2, google_container_node_pool.worker_default_nodepool_2]
}

# Outputs
output "manager_cluster_name" {
  description = "Name of the manager cluster"
  value       = google_container_cluster.manager_cluster.name
}

output "worker_cluster_names" {
  description = "Names of the worker clusters"
  value       = [
    google_container_cluster.worker_cluster.name,
    google_container_cluster.worker_cluster_2.name
  ]
}

# Cleanup kubectl contexts after all clusters are destroyed
resource "null_resource" "cleanup_kubeconfigs" {
  triggers = {
    always_run = timestamp()
    # Store context names for destroy provisioner
    manager_context = "${var.cluster_manager_name_prefix}-${var.location_manager}"
    manager_gke_context = "gke_${var.project_id}_${var.location_manager}_${var.cluster_manager_name_prefix}-${var.location_manager}"
    worker1_context = "${var.cluster_worker_names_prefix}-${var.region_worker}"
    worker1_gke_context = "gke_${var.project_id}_${var.region_worker}_${var.cluster_worker_names_prefix}-${var.region_worker}"
    worker2_context = "${var.cluster_worker_names_prefix}-${var.region_worker_2}"
    worker2_gke_context = "gke_${var.project_id}_${var.region_worker_2}_${var.cluster_worker_names_prefix}-${var.region_worker_2}"
  }

  provisioner "local-exec" {
    when    = destroy
    command = <<EOT
      # Clean up manager context
      kubectl config delete-context "${self.triggers.manager_context}" || true
      kubectl config delete-context "${self.triggers.manager_gke_context}" || true
      
      # Clean up first worker context
      kubectl config delete-context "${self.triggers.worker1_context}" || true
      kubectl config delete-context "${self.triggers.worker1_gke_context}" || true
      
      # Clean up second worker context
      kubectl config delete-context "${self.triggers.worker2_context}" || true
      kubectl config delete-context "${self.triggers.worker2_gke_context}" || true
      
      echo "Kubectl contexts cleaned up successfully"
    EOT
  }
}
