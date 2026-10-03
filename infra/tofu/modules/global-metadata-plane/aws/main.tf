# The global metadata plane, AWS reference implementation.
#
# The specification's data-ownership matrix puts tenant and security metadata in
# a "global metadata plane; no raw subscriber transaction data", and its
# availability section makes that plane multi-region "but raw resident tenant
# transaction/subscriber data is not centralized to achieve availability". That
# sentence is the whole design constraint: this is the one part of the estate
# that is deliberately multi-region, and it is allowed to be only because it
# holds identifiers and routing, never fiscal or personal content.
#
# What lives here:
#   cell directory   cell id -> region, residency jurisdiction, ingress
#                    hostname, lifecycle state
#   tenant routing   tenant id -> home cell id. Used by ingress to send a
#                    request to the cell that holds the tenant's data.
#   plane key        a multi-Region KMS key that encrypts only the two tables
#                    above. It is never used by a cell for cell data.
#
# What does not, and the review question for any addition: transactions,
# decisions, evidence, documents, addresses, names, amounts, content bundles.
# A cell keeps working on last-known-good routing if this plane is down (NFR
# §11); a cell never reads its own data from here.

data "aws_region" "current" {}
data "aws_caller_identity" "current" {}

locals {
  name = "ztax-${var.environment}-global"

  tags = merge(var.labels, {
    "ztax:plane"       = "global-metadata"
    "ztax:environment" = var.environment
    "ztax:train"       = "INFRA"
    "ztax:managed-by"  = "opentofu"
  })
}

resource "terraform_data" "region_guard" {
  input = var.primary_region

  lifecycle {
    precondition {
      condition     = data.aws_region.current.region == var.primary_region
      error_message = "The AWS provider must be configured for primary_region."
    }
  }
}

resource "aws_kms_key" "plane" {
  description             = "${local.name} metadata plane tables"
  key_usage               = "ENCRYPT_DECRYPT"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  multi_region            = true
  tags                    = local.tags

  depends_on = [terraform_data.region_guard]
}

resource "aws_kms_alias" "plane" {
  name          = "alias/${local.name}"
  target_key_id = aws_kms_key.plane.key_id
}

# Provider v6 per-resource region: one replica of the plane key per replica
# region. This is the only module in infra/ permitted to set it
# (infra/tools/check_tofu.py refuses it in the cell module).
resource "aws_kms_replica_key" "plane" {
  for_each = toset(var.replica_regions)

  region                  = each.value
  primary_key_arn         = aws_kms_key.plane.arn
  description             = "${local.name} metadata plane tables (replica)"
  deletion_window_in_days = 30
  tags                    = local.tags
}

locals {
  tables = {
    cell-directory = { hash_key = "cell_id" }
    tenant-routing = { hash_key = "tenant_id" }
  }
}

resource "aws_dynamodb_table" "this" {
  for_each = local.tables

  name         = "${local.name}-${each.key}"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = each.value.hash_key

  attribute {
    name = each.value.hash_key
    type = "S"
  }

  stream_enabled   = true
  stream_view_type = "NEW_AND_OLD_IMAGES"

  server_side_encryption {
    enabled     = true
    kms_key_arn = aws_kms_key.plane.arn
  }

  point_in_time_recovery {
    enabled = true
  }

  deletion_protection_enabled = true

  dynamic "replica" {
    for_each = toset(var.replica_regions)
    content {
      region_name            = replica.value
      kms_key_arn            = aws_kms_replica_key.plane[replica.value].arn
      point_in_time_recovery = true
      propagate_tags         = true
    }
  }

  tags = merge(local.tags, { "ztax:table" = each.key })
}
