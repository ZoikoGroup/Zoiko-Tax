# The cell contract's outputs (../contract.json). Shapes are provider-neutral;
# the values inside are this provider's identifiers and are opaque to callers.
# infra/kubernetes consumes these through the overlay for the cell.

output "cell_id" {
  description = "The cell identifier. ZTAX_CELL."
  value       = var.cell_id
}

output "region" {
  description = "The one region the cell lives in. ZTAX_REGION."
  value       = data.aws_region.current.region
}

output "network" {
  description = "Network facts the Kubernetes egress policy needs: the cell CIDR (the only destination a Go workload may reach) and the database subnets."
  value = {
    id             = aws_vpc.this.id
    cidr           = aws_vpc.this.cidr_block
    workload_cidrs = local.workload_cidrs
    database_cidrs = local.database_cidrs
  }
}

output "kubernetes" {
  description = "The cell's cluster."
  value = {
    cluster_name    = aws_eks_cluster.this.name
    api_endpoint    = aws_eks_cluster.this.endpoint
    oidc_issuer_url = local.oidc_issuer
  }
}

output "database" {
  description = "The cell's PostgreSQL. Connection facts only; credentials are in secret_refs."
  value = {
    host           = aws_db_instance.this.address
    port           = aws_db_instance.this.port
    name           = aws_db_instance.this.db_name
    postgres_major = var.database.postgres_major
  }
}

output "keys" {
  description = "Key identifiers by purpose (ADR-0017 §2.6). Identifiers, never key material."
  value = {
    storage  = aws_kms_key.storage.arn
    evidence = aws_kms_key.evidence.arn
    seal     = aws_kms_key.seal.arn
  }
}

output "evidence_store" {
  description = "The retention-locked evidence store."
  value = {
    name           = aws_s3_bucket.evidence.bucket
    lock_mode      = var.evidence.lock_mode
    retention_days = var.evidence.retention_days
  }
}

output "workload_identities" {
  description = "Workload identity per deployable, for the service-account annotation (IRSA: eks.amazonaws.com/role-arn)."
  value       = { for k, r in aws_iam_role.workload : k => r.arn }
}

output "secret_refs" {
  description = "Credential references in the form the binaries take (ZTAX_DATABASE_URL_REF). The scheme names the store; the path names the secret. Never a value."
  value       = { for k, s in aws_secretsmanager_secret.this : k => "aws-sm://${s.name}" }
}
