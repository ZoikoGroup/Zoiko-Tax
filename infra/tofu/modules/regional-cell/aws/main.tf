# One regional execution cell, AWS reference implementation.
#
# ADR-0009 §2.6: a cell is the unit of residency, deployment and failure, and
# cells share no database, no broker, no cache and no bundle-serving path. This
# module therefore owns everything it needs — network, keys, database, evidence
# store, cluster, secrets, workload identities — and takes nothing from another
# cell or from the global metadata plane. Two cells are two instantiations of
# this module in two state files, and neither can name the other's resources.
#
# Residency is enforced three ways, because the failure is silent:
#   1. variable validation — region inside the residency envelope, every zone
#      inside region;
#   2. the residency guard below — the provider this module was handed really
#      is configured for var.region, checked at plan time;
#   3. infra/tools/check_tofu.py — no resource in this module sets the AWS
#      provider's per-resource `region` argument and no region literal appears,
#      so nothing can be pointed at another region from inside the module.

data "aws_region" "current" {}
data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}

locals {
  name = "ztax-${var.cell_id}"

  tags = merge(var.labels, {
    "ztax:cell"         = var.cell_id
    "ztax:region"       = var.region
    "ztax:environment"  = var.environment
    "ztax:jurisdiction" = var.residency.jurisdiction
    "ztax:train"        = "INFRA"
    "ztax:managed-by"   = "opentofu"
  })

  account_id = data.aws_caller_identity.current.account_id
  partition  = data.aws_partition.current.partition

  # Provider-neutral size tiers mapped to this provider's classes. A second
  # provider supplies its own map; the tier names are the contract.
  db_instance_class = {
    small  = "db.t4g.medium"
    medium = "db.r7g.large"
    large  = "db.r7g.2xlarge"
  }[var.database.size]

  node_instance_types = {
    small  = ["m7i.large"]
    medium = ["m7i.xlarge"]
    large  = ["m7i.2xlarge"]
  }[var.kubernetes.node_size]

  # Four subnet tiers per zone, carved from the cell CIDR: workload (private,
  # NAT egress for nodes), database and endpoint (private, no route out at
  # all), edge (public: NAT and load balancers only, no workload is ever
  # scheduled there).
  zone_count       = length(var.zones)
  workload_cidrs   = [for i, _ in var.zones : cidrsubnet(var.network.cidr, 3, i)]
  database_cidrs   = [for i, _ in var.zones : cidrsubnet(var.network.cidr, 6, 32 + i)]
  edge_cidrs       = [for i, _ in var.zones : cidrsubnet(var.network.cidr, 6, 40 + i)]
  endpoint_cidrs   = [for i, _ in var.zones : cidrsubnet(var.network.cidr, 6, 48 + i)]
  eks_name         = "${local.name}-k8s"
  oidc_issuer      = aws_eks_cluster.this.identity[0].oidc[0].issuer
  oidc_issuer_host = replace(local.oidc_issuer, "https://", "")
}

# The residency guard. Variable validation proves the *declared* region is
# permitted; this proves the provider is actually configured for it. A root that
# declared an EU region but configured its provider for a US one would
# otherwise build an EU cell in the US with every label saying EU.
resource "terraform_data" "residency_guard" {
  input = var.region

  lifecycle {
    precondition {
      condition     = data.aws_region.current.region == var.region
      error_message = "The AWS provider is configured for a different region than var.region. A cell is built in exactly one region (ADR-0009 §2.6)."
    }
  }
}

check "residency" {
  assert {
    condition     = data.aws_region.current.region == var.region && contains(var.residency.permitted_regions, data.aws_region.current.region)
    error_message = "This cell's provider region is not its declared, permitted region."
  }
}
