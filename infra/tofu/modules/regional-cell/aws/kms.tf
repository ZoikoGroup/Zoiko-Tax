# The cell's own keys. ADR-0017 §2.6: distinct hierarchies for distinct
# purposes, and private key material never leaves the KMS. ADR-0017 §2.7: keys
# are per cell. None of these is a multi-Region key — a replica of a cell key in
# another region would be a residency hole in the one control residency most
# depends on.
#
#   storage   symmetric. Database, secrets, cluster secrets envelope, logs.
#   evidence  symmetric. The evidence object store only, so the evidence
#             store's access can be revoked or audited without touching the
#             database's.
#   seal      ECC_NIST_P384 sign/verify. Evidence and period seals over a
#             Merkle root (ADR-0011 §2.5). Asymmetric KMS keys do not rotate
#             in place; rotation is a new key and a new key identifier, and the
#             old one stays enabled for verification for the statutory period
#             (ADR-0017 §2.7, §6).
#
# Content-bundle signing keys are not here. They belong to the Z4 content
# build plane, which must not share a trust zone with anything that consumes
# bundles (ADR-0009 §2.2); a cell holds only the public keyring that verifies.

locals {
  key_admin_arn = "arn:${local.partition}:iam::${local.account_id}:root"
}

data "aws_iam_policy_document" "key_base" {
  statement {
    sid       = "AccountAdministration"
    effect    = "Allow"
    actions   = ["kms:*"]
    resources = ["*"]
    principals {
      type        = "AWS"
      identifiers = [local.key_admin_arn]
    }
  }
}

data "aws_iam_policy_document" "storage_key" {
  source_policy_documents = [data.aws_iam_policy_document.key_base.json]

  statement {
    sid       = "CloudWatchLogsInRegion"
    effect    = "Allow"
    actions   = ["kms:Encrypt", "kms:Decrypt", "kms:ReEncrypt*", "kms:GenerateDataKey*", "kms:Describe*"]
    resources = ["*"]
    principals {
      type        = "Service"
      identifiers = ["logs.${var.region}.amazonaws.com"]
    }
    condition {
      test     = "ArnLike"
      variable = "kms:EncryptionContext:aws:logs:arn"
      values   = ["arn:${local.partition}:logs:${var.region}:${local.account_id}:log-group:/ztax/${var.cell_id}/*"]
    }
  }
}

resource "aws_kms_key" "storage" {
  description             = "${local.name} storage: database, secrets, cluster secrets, logs"
  key_usage               = "ENCRYPT_DECRYPT"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  multi_region            = false
  policy                  = data.aws_iam_policy_document.storage_key.json
  tags                    = merge(local.tags, { "ztax:key-purpose" = "storage" })

  depends_on = [terraform_data.residency_guard]
}

resource "aws_kms_alias" "storage" {
  name          = "alias/${local.name}/storage"
  target_key_id = aws_kms_key.storage.key_id
}

resource "aws_kms_key" "evidence" {
  description             = "${local.name} evidence object store"
  key_usage               = "ENCRYPT_DECRYPT"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  multi_region            = false
  policy                  = data.aws_iam_policy_document.key_base.json
  tags                    = merge(local.tags, { "ztax:key-purpose" = "evidence" })

  depends_on = [terraform_data.residency_guard]
}

resource "aws_kms_alias" "evidence" {
  name          = "alias/${local.name}/evidence"
  target_key_id = aws_kms_key.evidence.key_id
}

resource "aws_kms_key" "seal" {
  description              = "${local.name} evidence seal signing, ECDSA P-384 (ADR-0011 §2.5)"
  key_usage                = "SIGN_VERIFY"
  customer_master_key_spec = "ECC_NIST_P384"
  deletion_window_in_days  = 30
  multi_region             = false
  policy                   = data.aws_iam_policy_document.key_base.json
  tags                     = merge(local.tags, { "ztax:key-purpose" = "seal" })

  depends_on = [terraform_data.residency_guard]

  # A seal key that is deleted takes the verifiability of every seal it made
  # with it. Retirement is disabling for signing, never deletion.
  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_kms_alias" "seal" {
  name          = "alias/${local.name}/seal"
  target_key_id = aws_kms_key.seal.key_id
}
