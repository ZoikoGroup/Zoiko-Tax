# Residency is the property of this module whose failure is silent: a cell
# built in the wrong region looks exactly like one built in the right region,
# down to its labels. These tests prove each guard refuses, against a mocked
# provider, so they run in CI with no cloud account.
#
#   tofu -chdir=infra/tofu/modules/regional-cell/aws init -backend=false
#   tofu -chdir=infra/tofu/modules/regional-cell/aws test

mock_provider "aws" {
  mock_data "aws_region" {
    defaults = {
      region = "eu-central-1"
    }
  }
  mock_data "aws_caller_identity" {
    defaults = {
      account_id = "111111111111"
    }
  }
  mock_data "aws_partition" {
    defaults = {
      partition = "aws"
    }
  }
  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{}"
    }
  }
  mock_resource "aws_eks_cluster" {
    defaults = {
      identity = [{ oidc = [{ issuer = "https://oidc.eks.eu-central-1.amazonaws.com/id/MOCK" }] }]
    }
  }
  mock_resource "aws_iam_openid_connect_provider" {
    defaults = {
      arn = "arn:aws:iam::111111111111:oidc-provider/oidc.eks.eu-central-1.amazonaws.com/id/MOCK"
    }
  }
  mock_resource "aws_kms_key" {
    defaults = {
      arn = "arn:aws:kms:eu-central-1:111111111111:key/mock"
    }
  }
  mock_resource "aws_s3_bucket" {
    defaults = {
      arn = "arn:aws:s3:::mock-evidence"
    }
  }
  mock_resource "aws_cloudwatch_log_group" {
    defaults = {
      arn = "arn:aws:logs:eu-central-1:111111111111:log-group:mock"
    }
  }
  mock_resource "aws_secretsmanager_secret" {
    defaults = {
      arn = "arn:aws:secretsmanager:eu-central-1:111111111111:secret:mock"
    }
  }
  mock_resource "aws_launch_template" {
    defaults = {
      id = "lt-0123456789abcdef0"
    }
  }
  mock_resource "aws_iam_role" {
    defaults = {
      arn = "arn:aws:iam::111111111111:role/mock"
    }
  }
}

variables {
  cell_id     = "euc1-test-01"
  environment = "dev"
  region      = "eu-central-1"
  residency = {
    jurisdiction      = "EU"
    permitted_regions = ["eu-central-1", "eu-west-1"]
  }
  zones   = ["eu-central-1a", "eu-central-1b"]
  network = { cidr = "10.40.0.0/16" }
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
  evidence = {
    retention_days = 30
    lock_mode      = "GOVERNANCE"
  }
}

run "a_cell_inside_its_envelope_plans" {
  command = plan

  assert {
    condition     = output.region == "eu-central-1"
    error_message = "the cell reports a region other than the one it was built in"
  }

  assert {
    condition     = output.secret_refs["app_database"] == "aws-sm://ztax/euc1-test-01/database/app"
    error_message = "the application credential reference is not the form ZTAX_DATABASE_URL_REF takes"
  }
}

run "a_region_outside_the_envelope_is_refused" {
  command = plan
  variables {
    region = "us-east-1"
    zones  = ["us-east-1a", "us-east-1b"]
  }
  # The provider really is in us-east-1, so only the envelope can refuse.
  override_data {
    target = data.aws_region.current
    values = { region = "us-east-1" }
  }
  expect_failures = [var.residency]
}

run "a_zone_in_another_region_is_refused" {
  command = plan
  variables {
    zones = ["eu-central-1a", "eu-west-1b"]
  }
  expect_failures = [var.zones]
}

run "a_provider_configured_for_another_region_is_refused" {
  command = plan
  variables {
    region = "eu-west-1"
    zones  = ["eu-west-1a", "eu-west-1b"]
  }
  # The mocked provider reports eu-central-1: the declared region is permitted,
  # but the provider would build somewhere else.
  expect_failures = [terraform_data.residency_guard, check.residency]
}

run "production_requires_compliance_lock_three_zones_and_ha" {
  command = plan
  variables {
    environment = "production"
  }
  expect_failures = [var.zones, var.database, var.evidence]
}
