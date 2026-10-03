# The regional immutable evidence store (ADR-0011, ADR-0003, W1 lane D).
#
# "A seal over mutable data is not a seal." Object Lock is enabled at creation
# (it cannot be added later), versioning is on (Object Lock requires it), and a
# default retention applies to every object written. In COMPLIANCE mode no
# principal — including the account root — can shorten the retention or delete
# a locked version before it expires.
#
# No replication configuration. Cross-region replication of evidence is a
# cross-cell transfer and must go through the explicit evidenced-transfer path
# (ADR-0009 §2.6), not a bucket setting.

resource "aws_s3_bucket" "evidence" {
  bucket              = "${local.name}-evidence-${local.account_id}"
  object_lock_enabled = true
  force_destroy       = false
  tags                = merge(local.tags, { "ztax:store" = "evidence" })

  depends_on = [terraform_data.residency_guard]

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_versioning" "evidence" {
  bucket = aws_s3_bucket.evidence.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_object_lock_configuration" "evidence" {
  bucket = aws_s3_bucket.evidence.id

  rule {
    default_retention {
      mode = var.evidence.lock_mode
      days = var.evidence.retention_days
    }
  }

  depends_on = [aws_s3_bucket_versioning.evidence]
}

resource "aws_s3_bucket_server_side_encryption_configuration" "evidence" {
  bucket = aws_s3_bucket.evidence.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = aws_kms_key.evidence.arn
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_public_access_block" "evidence" {
  bucket                  = aws_s3_bucket.evidence.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "evidence" {
  bucket = aws_s3_bucket.evidence.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

data "aws_iam_policy_document" "evidence_bucket" {
  statement {
    sid       = "DenyInsecureTransport"
    effect    = "Deny"
    actions   = ["s3:*"]
    resources = [aws_s3_bucket.evidence.arn, "${aws_s3_bucket.evidence.arn}/*"]
    principals {
      type        = "*"
      identifiers = ["*"]
    }
    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }

  statement {
    sid       = "DenyWrongKey"
    effect    = "Deny"
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.evidence.arn}/*"]
    principals {
      type        = "*"
      identifiers = ["*"]
    }
    condition {
      test     = "StringNotEqualsIfExists"
      variable = "s3:x-amz-server-side-encryption-aws-kms-key-id"
      values   = [aws_kms_key.evidence.arn]
    }
  }

  # Belt and braces over Object Lock: nobody may delete a version, bypass a
  # GOVERNANCE lock or add replication. Object Lock already makes the deletes
  # fail in COMPLIANCE mode; this makes the attempt an explicit deny that shows
  # up as such in CloudTrail. The lock configuration itself is left writable so
  # that retention can be lengthened by a reviewed change; it cannot be
  # shortened for objects already written.
  statement {
    sid    = "DenyRetentionTampering"
    effect = "Deny"
    actions = [
      "s3:DeleteObjectVersion",
      "s3:BypassGovernanceRetention",
      "s3:DeleteBucket",
      "s3:PutReplicationConfiguration",
    ]
    resources = [aws_s3_bucket.evidence.arn, "${aws_s3_bucket.evidence.arn}/*"]
    principals {
      type        = "*"
      identifiers = ["*"]
    }
  }
}

resource "aws_s3_bucket_policy" "evidence" {
  bucket = aws_s3_bucket.evidence.id
  policy = data.aws_iam_policy_document.evidence_bucket.json

  depends_on = [aws_s3_bucket_public_access_block.evidence]
}
