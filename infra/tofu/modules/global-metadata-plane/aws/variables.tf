variable "environment" {
  description = "dev, staging or production. One metadata plane per environment."
  type        = string

  validation {
    condition     = contains(["dev", "staging", "production"], var.environment)
    error_message = "environment must be dev, staging or production."
  }
}

variable "primary_region" {
  description = "The region writes are made in. The provider passed to this module must be configured for it."
  type        = string

  validation {
    condition     = can(regex("^[a-z]{2}(-[a-z]+)+-[0-9]$", var.primary_region))
    error_message = "primary_region must be a provider region identifier such as eu-central-1."
  }
}

variable "replica_regions" {
  description = "Regions holding read replicas of the plane. Multi-region is permitted here, and only here, because the plane holds no resident data."
  type        = list(string)

  validation {
    condition     = alltrue([for r in var.replica_regions : can(regex("^[a-z]{2}(-[a-z]+)+-[0-9]$", r))])
    error_message = "every replica region must be a provider region identifier."
  }

  validation {
    condition     = !contains(var.replica_regions, var.primary_region) && length(distinct(var.replica_regions)) == length(var.replica_regions)
    error_message = "replica_regions must be distinct and must not include primary_region."
  }
}

variable "labels" {
  description = "Extra labels/tags applied to every resource."
  type        = map(string)
  default     = {}
}
