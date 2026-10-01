# One development cell in the EU residency envelope.
#
# A root per cell, and a state file per cell, held in a bucket in the cell's
# own region: two cells never share state, so neither can read the other's
# outputs, and a mistake planning one cannot touch the other (ADR-0009 §2.6).
#
#   tofu init -backend-config=backend.hcl   # bucket/key/region for this cell
#   tofu plan -var account_id=<12 digits>

terraform {
  required_version = ">= 1.9.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }

  # Partial configuration; the bucket must be in this cell's region.
  backend "s3" {}
}

variable "account_id" {
  description = "The AWS account this cell is built in. The provider refuses any other, so a mis-selected profile fails before it plans."
  type        = string

  validation {
    condition     = can(regex("^[0-9]{12}$", var.account_id))
    error_message = "account_id is a 12-digit AWS account number."
  }
}

locals {
  region = "eu-central-1"
}

provider "aws" {
  region              = local.region
  allowed_account_ids = [var.account_id]

  default_tags {
    tags = {
      "ztax:root" = "envs/dev/cell-euc1-dev-01"
    }
  }
}

module "cell" {
  source = "../../../modules/regional-cell/aws"

  cell_id     = "euc1-dev-01"
  environment = "dev"
  region      = local.region

  residency = {
    jurisdiction      = "EU"
    permitted_regions = ["eu-central-1", "eu-west-1", "eu-west-3", "eu-north-1"]
  }

  zones = ["eu-central-1a", "eu-central-1b"]

  network = {
    cidr = "10.40.0.0/16"
  }

  database = {
    postgres_major        = 17
    size                  = "small"
    storage_gb            = 50
    highly_available      = false
    backup_retention_days = 7
  }

  kubernetes = {
    version             = "1.33"
    node_size           = "small"
    min_nodes           = 2
    max_nodes           = 4
    public_api_endpoint = false
  }

  # GOVERNANCE in dev, so a development bucket can be emptied by a privileged
  # operator. Production is COMPLIANCE and the module refuses anything else.
  evidence = {
    retention_days = 30
    lock_mode      = "GOVERNANCE"
  }

  workload_namespace = "ztax-cell"
}

output "cell" {
  description = "The cell contract outputs, consumed by infra/kubernetes/overlays/euc1-dev-01."
  value = {
    cell_id             = module.cell.cell_id
    region              = module.cell.region
    network             = module.cell.network
    kubernetes          = module.cell.kubernetes
    database            = module.cell.database
    keys                = module.cell.keys
    evidence_store      = module.cell.evidence_store
    workload_identities = module.cell.workload_identities
    secret_refs         = module.cell.secret_refs
  }
}
