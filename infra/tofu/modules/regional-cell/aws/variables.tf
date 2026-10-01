# The cell contract's inputs (../contract.json). Every variable here is part of
# the provider-neutral interface: no AWS instance type, ARN or account number
# appears in an input, because a second provider has to be able to take the
# same values. Provider-specific choices are made from these inside the module
# (see the size maps in locals.tf).

variable "cell_id" {
  description = "Stable cell identifier, e.g. euc1-dev-01. Becomes ZTAX_CELL and prefixes every resource name. Never reused for another cell."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9]{1,9}-[a-z]+-[0-9]{2}$", var.cell_id))
    error_message = "cell_id must look like <region-code>-<environment>-<nn>, e.g. euc1-dev-01."
  }
}

variable "environment" {
  description = "Deployment environment. Becomes ZTAX_ENVIRONMENT. A cloud cell is never 'development': that value unlocks local:// secret references and plain-HTTP affordances in the binary."
  type        = string

  validation {
    condition     = contains(["dev", "staging", "production"], var.environment)
    error_message = "environment must be dev, staging or production."
  }
}

variable "region" {
  description = "The one region this cell lives in. Every resource the module creates is created here and nowhere else (ADR-0009 §2.6, residency). Becomes ZTAX_REGION."
  type        = string

  validation {
    condition     = can(regex("^[a-z]{2}(-[a-z]+)+-[0-9]$", var.region))
    error_message = "region must be a provider region identifier (<geo>-<area>-<n>)."
  }
}

variable "residency" {
  description = "The residency envelope the cell is placed under. region must be one of permitted_regions; jurisdiction is recorded on every resource as a label."
  type = object({
    jurisdiction      = string
    permitted_regions = list(string)
  })

  validation {
    condition     = length(var.residency.permitted_regions) > 0 && var.residency.jurisdiction != ""
    error_message = "residency needs a jurisdiction and at least one permitted region."
  }

  validation {
    condition     = contains(var.residency.permitted_regions, var.region)
    error_message = "region is outside the residency envelope: it must be one of residency.permitted_regions."
  }
}

variable "zones" {
  description = "Availability zones inside region. Production cells span at least three (NFR §14: no single-zone runtime dependency)."
  type        = list(string)

  validation {
    condition     = length(var.zones) >= 2 && length(var.zones) <= 4 && length(distinct(var.zones)) == length(var.zones)
    error_message = "zones needs two to four distinct availability zones (the subnet plan in main.tf divides the cell CIDR for at most four)."
  }

  validation {
    condition     = alltrue([for z in var.zones : startswith(z, var.region)])
    error_message = "every zone must belong to region; a zone in another region is a residency violation."
  }

  validation {
    condition     = var.environment != "production" || length(var.zones) >= 3
    error_message = "a production cell spans at least three zones."
  }
}

variable "network" {
  description = "The cell's private network. cidr is the whole cell address space; nothing outside it is reachable from a Go workload (infra/kubernetes, ADR-0006 §2.1)."
  type = object({
    cidr = string
  })

  validation {
    condition     = can(cidrhost(var.network.cidr, 0)) && tonumber(split("/", var.network.cidr)[1]) <= 18
    error_message = "network.cidr must be a valid CIDR of /18 or larger, so it can be divided into per-zone subnets."
  }
}

variable "database" {
  description = "The cell's own PostgreSQL (ADR-0008 §2.1). size is a provider-neutral tier mapped to an instance class inside the module."
  type = object({
    postgres_major        = number
    size                  = string
    storage_gb            = number
    highly_available      = bool
    backup_retention_days = number
  })

  validation {
    condition     = var.database.postgres_major == 17
    error_message = "ADR-0008 §2.1 fixes PostgreSQL 17."
  }

  validation {
    condition     = contains(["small", "medium", "large"], var.database.size)
    error_message = "database.size must be small, medium or large."
  }

  validation {
    condition     = var.database.backup_retention_days >= 7 && var.database.backup_retention_days <= 35
    error_message = "database.backup_retention_days must be between 7 and 35."
  }

  validation {
    condition     = var.environment != "production" || var.database.highly_available
    error_message = "a production cell's database is highly available (NFR §14: the C0 datastore tolerates a single-zone failure)."
  }
}

variable "kubernetes" {
  description = "The cell's own cluster. node_size is a provider-neutral tier."
  type = object({
    version             = string
    node_size           = string
    min_nodes           = number
    max_nodes           = number
    public_api_endpoint = bool
  })

  validation {
    condition     = can(regex("^1\\.[0-9]+$", var.kubernetes.version))
    error_message = "kubernetes.version is a minor version such as 1.33."
  }

  validation {
    condition     = contains(["small", "medium", "large"], var.kubernetes.node_size)
    error_message = "kubernetes.node_size must be small, medium or large."
  }

  validation {
    condition     = var.kubernetes.min_nodes >= 1 && var.kubernetes.max_nodes >= var.kubernetes.min_nodes
    error_message = "kubernetes needs min_nodes >= 1 and max_nodes >= min_nodes."
  }
}

variable "evidence" {
  description = "The regional immutable evidence store (ADR-0011, W1 lane D). Objects are write-once for retention_days under the lock mode; COMPLIANCE cannot be shortened or removed by anyone, including the account root."
  type = object({
    retention_days = number
    lock_mode      = string
  })

  validation {
    condition     = contains(["COMPLIANCE", "GOVERNANCE"], var.evidence.lock_mode)
    error_message = "evidence.lock_mode must be COMPLIANCE or GOVERNANCE."
  }

  validation {
    condition     = var.evidence.retention_days >= 1
    error_message = "evidence.retention_days must be at least one day."
  }

  validation {
    condition     = var.environment != "production" || var.evidence.lock_mode == "COMPLIANCE"
    error_message = "a production cell's evidence lock is COMPLIANCE: a seal over deletable evidence is not a seal."
  }
}

variable "workload_namespace" {
  description = "The Kubernetes namespace the cell's workloads run in. Workload identities trust service accounts in this namespace only."
  type        = string
  default     = "ztax-cell"
}

variable "labels" {
  description = "Extra labels/tags applied to every resource."
  type        = map(string)
  default     = {}
}
