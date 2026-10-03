# The cell's own network. Not peered, not attached to a transit gateway, not
# shared: a cell reaches another cell only through an explicit evidenced
# transfer (ADR-0009 §2.6), never through a route.

resource "aws_vpc" "this" {
  cidr_block           = var.network.cidr
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = merge(local.tags, { Name = local.name })

  depends_on = [terraform_data.residency_guard]
}

# Nothing is allowed by the default security group. Every security group the
# module needs is named and scoped below.
resource "aws_default_security_group" "this" {
  vpc_id = aws_vpc.this.id
  tags   = merge(local.tags, { Name = "${local.name}-default-deny" })
}

resource "aws_subnet" "workload" {
  count             = local.zone_count
  vpc_id            = aws_vpc.this.id
  cidr_block        = local.workload_cidrs[count.index]
  availability_zone = var.zones[count.index]

  tags = merge(local.tags, {
    Name                              = "${local.name}-workload-${var.zones[count.index]}"
    "ztax:tier"                       = "workload"
    "kubernetes.io/role/internal-elb" = "1"
  })
}

resource "aws_subnet" "database" {
  count             = local.zone_count
  vpc_id            = aws_vpc.this.id
  cidr_block        = local.database_cidrs[count.index]
  availability_zone = var.zones[count.index]

  tags = merge(local.tags, {
    Name        = "${local.name}-database-${var.zones[count.index]}"
    "ztax:tier" = "database"
  })
}

resource "aws_subnet" "endpoint" {
  count             = local.zone_count
  vpc_id            = aws_vpc.this.id
  cidr_block        = local.endpoint_cidrs[count.index]
  availability_zone = var.zones[count.index]

  tags = merge(local.tags, {
    Name        = "${local.name}-endpoint-${var.zones[count.index]}"
    "ztax:tier" = "endpoint"
  })
}

resource "aws_subnet" "edge" {
  count             = local.zone_count
  vpc_id            = aws_vpc.this.id
  cidr_block        = local.edge_cidrs[count.index]
  availability_zone = var.zones[count.index]

  tags = merge(local.tags, {
    Name                     = "${local.name}-edge-${var.zones[count.index]}"
    "ztax:tier"              = "edge"
    "kubernetes.io/role/elb" = "1"
  })
}

# --- egress for nodes ---------------------------------------------------------
#
# Nodes pull signed images from the registry and so need a route out. Pods do
# not inherit it: infra/kubernetes denies every Go workload egress beyond the
# cell CIDR, which is how ADR-0006 §2.1's "no direct provider call" is enforced
# at the network layer rather than by review.

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id
  tags   = merge(local.tags, { Name = local.name })
}

resource "aws_eip" "nat" {
  count  = local.zone_count
  domain = "vpc"
  tags   = merge(local.tags, { Name = "${local.name}-nat-${var.zones[count.index]}" })
}

# One NAT per zone, so losing a zone does not take egress from the others
# (NFR §14).
resource "aws_nat_gateway" "this" {
  count         = local.zone_count
  allocation_id = aws_eip.nat[count.index].id
  subnet_id     = aws_subnet.edge[count.index].id
  tags          = merge(local.tags, { Name = "${local.name}-${var.zones[count.index]}" })

  depends_on = [aws_internet_gateway.this]
}

resource "aws_route_table" "edge" {
  vpc_id = aws_vpc.this.id
  tags   = merge(local.tags, { Name = "${local.name}-edge" })
}

resource "aws_route" "edge_default" {
  route_table_id         = aws_route_table.edge.id
  destination_cidr_block = "0.0.0.0/0"
  gateway_id             = aws_internet_gateway.this.id
}

resource "aws_route_table_association" "edge" {
  count          = local.zone_count
  subnet_id      = aws_subnet.edge[count.index].id
  route_table_id = aws_route_table.edge.id
}

resource "aws_route_table" "workload" {
  count  = local.zone_count
  vpc_id = aws_vpc.this.id
  tags   = merge(local.tags, { Name = "${local.name}-workload-${var.zones[count.index]}" })
}

resource "aws_route" "workload_default" {
  count                  = local.zone_count
  route_table_id         = aws_route_table.workload[count.index].id
  destination_cidr_block = "0.0.0.0/0"
  nat_gateway_id         = aws_nat_gateway.this[count.index].id
}

resource "aws_route_table_association" "workload" {
  count          = local.zone_count
  subnet_id      = aws_subnet.workload[count.index].id
  route_table_id = aws_route_table.workload[count.index].id
}

# The database and endpoint tiers have no default route at all. A database that
# can open a connection to the internet is one exfiltration step from a breach.
resource "aws_route_table" "isolated" {
  vpc_id = aws_vpc.this.id
  tags   = merge(local.tags, { Name = "${local.name}-isolated" })
}

resource "aws_route_table_association" "database" {
  count          = local.zone_count
  subnet_id      = aws_subnet.database[count.index].id
  route_table_id = aws_route_table.isolated.id
}

resource "aws_route_table_association" "endpoint" {
  count          = local.zone_count
  subnet_id      = aws_subnet.endpoint[count.index].id
  route_table_id = aws_route_table.isolated.id
}

# --- private endpoints --------------------------------------------------------
#
# KMS, Secrets Manager and STS are reached on addresses inside the cell CIDR,
# with private DNS, so the regional service name resolves to an in-cell
# address. That is what lets the Kubernetes egress policy allow them while
# denying everything outside the cell. Only the cell's own region's service
# names are created — the endpoint service name is built from var.region.
#
# Deliberately absent: bedrock, bedrock-runtime, bedrock-agent-runtime and
# sagemaker.runtime. An interface endpoint for a model provider inside the cell
# would put that provider inside the CIDR the Go workloads may reach, and
# check_tofu.py refuses one.

locals {
  interface_endpoints = toset(["kms", "secretsmanager", "sts", "logs", "ecr.api", "ecr.dkr"])
}

resource "aws_security_group" "endpoints" {
  name        = "${local.name}-endpoints"
  description = "HTTPS to the cell's private service endpoints from inside the cell only"
  vpc_id      = aws_vpc.this.id
  tags        = local.tags
}

resource "aws_vpc_security_group_ingress_rule" "endpoints_https" {
  security_group_id = aws_security_group.endpoints.id
  description       = "HTTPS from the cell CIDR"
  cidr_ipv4         = var.network.cidr
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
}

resource "aws_vpc_endpoint" "interface" {
  for_each = local.interface_endpoints

  vpc_id              = aws_vpc.this.id
  service_name        = "com.amazonaws.${var.region}.${each.key}"
  vpc_endpoint_type   = "Interface"
  private_dns_enabled = true
  subnet_ids          = aws_subnet.endpoint[*].id
  security_group_ids  = [aws_security_group.endpoints.id]

  tags = merge(local.tags, { Name = "${local.name}-${each.key}" })
}

resource "aws_vpc_endpoint" "s3" {
  vpc_id            = aws_vpc.this.id
  service_name      = "com.amazonaws.${var.region}.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = concat(aws_route_table.workload[*].id, [aws_route_table.isolated.id])

  tags = merge(local.tags, { Name = "${local.name}-s3" })
}

# --- flow logs ----------------------------------------------------------------
#
# The negative test for ADR-0006 §2.1 (a Go pod cannot reach a provider) needs
# something that records the attempt. Flow logs stay in the cell's region,
# encrypted under the cell's storage key.

resource "aws_cloudwatch_log_group" "flow" {
  name              = "/ztax/${var.cell_id}/vpc-flow"
  retention_in_days = 90
  kms_key_id        = aws_kms_key.storage.arn
  tags              = local.tags
}

resource "aws_iam_role" "flow" {
  name = "${local.name}-vpc-flow"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "vpc-flow-logs.amazonaws.com" }
      Action    = "sts:AssumeRole"
      Condition = { StringEquals = { "aws:SourceAccount" = local.account_id } }
    }]
  })
  tags = local.tags
}

resource "aws_iam_role_policy" "flow" {
  name = "write-flow-logs"
  role = aws_iam_role.flow.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["logs:CreateLogStream", "logs:PutLogEvents", "logs:DescribeLogStreams"]
      Resource = "${aws_cloudwatch_log_group.flow.arn}:*"
    }]
  })
}

resource "aws_flow_log" "this" {
  vpc_id          = aws_vpc.this.id
  traffic_type    = "ALL"
  log_destination = aws_cloudwatch_log_group.flow.arn
  iam_role_arn    = aws_iam_role.flow.arn
  tags            = local.tags
}
