# The cell's own secrets. Declared here, populated out of band.
#
# There is no aws_secretsmanager_secret_version in this module and there must
# never be one: a secret value written by OpenTofu is a secret value in the
# state file, and the state file is read by every pipeline that plans this
# cell. ADR-0017 §2.4 — configuration names where a secret lives; it never
# carries it. The workloads receive the *name* of each secret as a reference
# (ZTAX_DATABASE_URL_REF=aws-sm://<name>) and resolve it at process start under
# their own workload identity.
#
# Authority-filing credentials are not here either. ADR-0017 §2.5 puts them in
# a separate namespace reachable only by ztax-adapter-* deployables, which
# arrive with their own module.

locals {
  secrets = {
    # DSN for the application role: INSERT/SELECT on evidence-bearing tables,
    # no DDL (ADR-0008 §2.8). Read by ztax-core and ztax-outbox-relay.
    app_database = "ztax/${var.cell_id}/database/app"
    # DSN for the migration role, which holds DDL. Read by ztax-migrate only.
    migrate_database = "ztax/${var.cell_id}/database/migrate"
  }
}

resource "aws_secretsmanager_secret" "this" {
  for_each = local.secrets

  name        = each.value
  description = "${local.name} ${each.key} credential. Value set out of band; never by OpenTofu."
  kms_key_id  = aws_kms_key.storage.arn

  recovery_window_in_days = 30

  tags = merge(local.tags, { "ztax:secret" = each.key })

  depends_on = [terraform_data.residency_guard]
}
