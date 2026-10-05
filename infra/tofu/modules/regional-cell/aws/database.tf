# The cell's own PostgreSQL 17 (ADR-0008 §2.1). One database per cell, sharing
# nothing. PostGIS is an extension of the engine and is created by migration
# 000001, not here — the schema is the MIGRATION train's, not INFRA's.
#
# No cross-region read replica and no cross-region automated-backup
# replication: either would put a resident copy of fiscal data in another
# region. Disaster recovery stays inside the residency envelope (NFR doctrine:
# "disaster recovery may not silently fail data into a prohibited geography").

resource "aws_db_subnet_group" "this" {
  name       = local.name
  subnet_ids = aws_subnet.database[*].id
  tags       = local.tags
}

resource "aws_security_group" "database" {
  name        = "${local.name}-database"
  description = "PostgreSQL from the cell's workload subnets only"
  vpc_id      = aws_vpc.this.id
  tags        = local.tags
}

resource "aws_vpc_security_group_ingress_rule" "database_from_workloads" {
  for_each = toset(local.workload_cidrs)

  security_group_id = aws_security_group.database.id
  description       = "PostgreSQL from a workload subnet"
  cidr_ipv4         = each.value
  ip_protocol       = "tcp"
  from_port         = 5432
  to_port           = 5432
}

resource "aws_db_parameter_group" "this" {
  name   = "${local.name}-pg${var.database.postgres_major}"
  family = "postgres${var.database.postgres_major}"
  tags   = local.tags

  # TLS on every connection. The DSNs held in Secrets Manager say
  # sslmode=verify-full; this makes the server refuse anything less.
  parameter {
    name  = "rds.force_ssl"
    value = "1"
  }

  # Statement logging would put fiscal values in the database log. Errors and
  # DDL only (ADR-0015 §2.3).
  parameter {
    name  = "log_statement"
    value = "ddl"
  }
}

resource "aws_db_instance" "this" {
  identifier     = local.name
  engine         = "postgres"
  engine_version = tostring(var.database.postgres_major)
  instance_class = local.db_instance_class

  db_name  = "ztax"
  username = "ztax_owner"

  # The master credential is generated and held by Secrets Manager under the
  # cell's storage key. It never passes through OpenTofu state, a variable or a
  # pipeline log. It is the bootstrap identity only: the application (DML) and
  # migration (DDL) roles are separate (ADR-0008 §2.8, §2.11).
  manage_master_user_password   = true
  master_user_secret_kms_key_id = aws_kms_key.storage.key_id

  iam_database_authentication_enabled = true

  allocated_storage     = var.database.storage_gb
  max_allocated_storage = var.database.storage_gb * 4
  storage_type          = "gp3"
  storage_encrypted     = true
  kms_key_id            = aws_kms_key.storage.arn

  multi_az               = var.database.highly_available
  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.database.id]
  publicly_accessible    = false
  parameter_group_name   = aws_db_parameter_group.this.name

  backup_retention_period  = var.database.backup_retention_days
  delete_automated_backups = false
  copy_tags_to_snapshot    = true
  deletion_protection      = true
  skip_final_snapshot      = false

  final_snapshot_identifier  = "${local.name}-final"
  auto_minor_version_upgrade = true

  performance_insights_enabled    = true
  performance_insights_kms_key_id = aws_kms_key.storage.arn
  enabled_cloudwatch_logs_exports = ["postgresql"]

  tags = local.tags

  depends_on = [terraform_data.residency_guard]
}
