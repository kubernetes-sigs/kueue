variable "project_id" {
  type        = string
  description = "GCP project ID"
}

variable "location_manager" {
  type        = string
  description = "Location of GKE cluster"
  default     = "europe-west4-a"
}
variable "cluster_manager_name_prefix" {
  type        = string
  description = "Prefix of MultiKueue Manager GKE cluster"
  default     = "manager"
}

variable "region_worker" {
  type    = string
  default = "us-east4-a"
}

variable "region_worker_2" {
  type    = string
  default = "asia-southeast1-a"
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

