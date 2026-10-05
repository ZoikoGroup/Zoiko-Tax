# Workload identities (ADR-0017 §2.4): each deployable gets short-lived
# credentials for exactly what it needs, through its own Kubernetes service
# account, and nothing else.
#
#   ztax-core          reads the application DSN; writes evidence (never
#                      deletes); signs seals with the seal key.
#   ztax-outbox-relay  reads the application DSN. Nothing else.
#   ztax-migrate       reads the migration (DDL) DSN. Nothing else — and in
#                      particular not the application's, so a migration cannot
#                      run as the application principal or vice versa
#                      (ADR-0008 §2.11).
#
# A role trusts one service account in one namespace of this cell's cluster.
# Authority-filing credentials (ADR-0017 §2.5) are reachable by none of these.

locals {
  workloads = {
    "ztax-core"         = { secrets = ["app_database"], evidence = true, seal = true }
    "ztax-outbox-relay" = { secrets = ["app_database"], evidence = false, seal = false }
    "ztax-migrate"      = { secrets = ["migrate_database"], evidence = false, seal = false }
  }
}

data "aws_iam_policy_document" "workload_assume" {
  for_each = local.workloads

  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.this.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.oidc_issuer_host}:sub"
      values   = ["system:serviceaccount:${var.workload_namespace}:${each.key}"]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.oidc_issuer_host}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "workload" {
  for_each = local.workloads

  name                 = "${local.name}-${each.key}"
  assume_role_policy   = data.aws_iam_policy_document.workload_assume[each.key].json
  max_session_duration = 3600
  tags                 = merge(local.tags, { "ztax:workload" = each.key })
}

data "aws_iam_policy_document" "workload" {
  for_each = local.workloads

  statement {
    sid       = "ReadOwnSecrets"
    actions   = ["secretsmanager:GetSecretValue", "secretsmanager:DescribeSecret"]
    resources = [for s in each.value.secrets : aws_secretsmanager_secret.this[s].arn]
  }

  statement {
    sid       = "DecryptOwnSecrets"
    actions   = ["kms:Decrypt"]
    resources = [aws_kms_key.storage.arn]
    condition {
      test     = "StringEquals"
      variable = "kms:ViaService"
      values   = ["secretsmanager.${var.region}.amazonaws.com"]
    }
  }

  dynamic "statement" {
    for_each = each.value.evidence ? [1] : []
    content {
      sid = "WriteOnceEvidence"
      # No DeleteObject, no DeleteObjectVersion, no PutObjectRetention: the
      # application can add evidence and read it back, nothing more.
      actions   = ["s3:PutObject", "s3:GetObject", "s3:GetObjectVersion", "s3:GetObjectRetention"]
      resources = ["${aws_s3_bucket.evidence.arn}/*"]
    }
  }

  dynamic "statement" {
    for_each = each.value.evidence ? [1] : []
    content {
      sid       = "EvidenceKey"
      actions   = ["kms:GenerateDataKey", "kms:Decrypt"]
      resources = [aws_kms_key.evidence.arn]
      condition {
        test     = "StringEquals"
        variable = "kms:ViaService"
        values   = ["s3.${var.region}.amazonaws.com"]
      }
    }
  }

  dynamic "statement" {
    for_each = each.value.seal ? [1] : []
    content {
      sid       = "SealSigning"
      actions   = ["kms:Sign", "kms:GetPublicKey", "kms:DescribeKey"]
      resources = [aws_kms_key.seal.arn]
      condition {
        test     = "StringEquals"
        variable = "kms:SigningAlgorithm"
        values   = ["ECDSA_SHA_384"]
      }
    }
  }
}

resource "aws_iam_role_policy" "workload" {
  for_each = local.workloads

  name   = "cell-access"
  role   = aws_iam_role.workload[each.key].id
  policy = data.aws_iam_policy_document.workload[each.key].json
}
