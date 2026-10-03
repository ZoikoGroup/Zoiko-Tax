# The development global metadata plane. Its own root and its own state: the
# plane and the cells never share a state file, so a cell's plan cannot read
# or change the plane and the plane cannot reach into a cell.

terraform {
  required_version = ">= 1.9.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }

  backend "s3" {}
}

variable "account_id" {
  description = "The AWS account the plane is built in."
  type        = string

  validation {
    condition     = can(regex("^[0-9]{12}$", var.account_id))
    error_message = "account_id is a 12-digit AWS account number."
  }
}

provider "aws" {
  region              = "eu-central-1"
  allowed_account_ids = [var.account_id]

  default_tags {
    tags = {
      "ztax:root" = "envs/dev/global-metadata-plane"
    }
  }
}

module "plane" {
  source = "../../../modules/global-metadata-plane/aws"

  environment     = "dev"
  primary_region  = "eu-central-1"
  replica_regions = ["eu-west-1"]
}

output "plane" {
  value = {
    primary_region = module.plane.primary_region
    tables         = module.plane.tables
    key            = module.plane.key
  }
}
