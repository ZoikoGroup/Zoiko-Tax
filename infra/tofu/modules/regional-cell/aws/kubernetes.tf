# The cell's own Kubernetes cluster.
#
# Self-managed add-ons are not bootstrapped: the AWS VPC CNI and kube-proxy are
# not installed, because the cell runs Cilium (infra/kubernetes/README.md). The
# reason is ADR-0006 §2.1 — Go workloads are denied egress to model-provider
# endpoints at the network layer, and the cell's allow-list names regional
# service endpoints by DNS name, which vanilla NetworkPolicy cannot express.
# Cilium, CoreDNS, Kyverno and the policy set under policy/ are installed by the
# cluster bootstrap before any workload namespace is labelled for admission.

data "aws_iam_policy_document" "eks_assume" {
  statement {
    actions = ["sts:AssumeRole", "sts:TagSession"]
    principals {
      type        = "Service"
      identifiers = ["eks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "cluster" {
  name               = "${local.eks_name}-cluster"
  assume_role_policy = data.aws_iam_policy_document.eks_assume.json
  tags               = local.tags
}

resource "aws_iam_role_policy_attachment" "cluster" {
  role       = aws_iam_role.cluster.name
  policy_arn = "arn:${local.partition}:iam::aws:policy/AmazonEKSClusterPolicy"
}

resource "aws_cloudwatch_log_group" "cluster" {
  name              = "/aws/eks/${local.eks_name}/cluster"
  retention_in_days = 90
  kms_key_id        = aws_kms_key.storage.arn
  tags              = local.tags
}

resource "aws_security_group" "cluster" {
  name        = "${local.eks_name}-control-plane"
  description = "EKS control plane, reachable from the cell only"
  vpc_id      = aws_vpc.this.id
  tags        = local.tags
}

resource "aws_vpc_security_group_ingress_rule" "cluster_api" {
  security_group_id = aws_security_group.cluster.id
  description       = "Kubernetes API from inside the cell"
  cidr_ipv4         = var.network.cidr
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
}

resource "aws_eks_cluster" "this" {
  name     = local.eks_name
  version  = var.kubernetes.version
  role_arn = aws_iam_role.cluster.arn

  bootstrap_self_managed_addons = false

  vpc_config {
    subnet_ids              = aws_subnet.workload[*].id
    security_group_ids      = [aws_security_group.cluster.id]
    endpoint_private_access = true
    endpoint_public_access  = var.kubernetes.public_api_endpoint
  }

  access_config {
    authentication_mode                         = "API"
    bootstrap_cluster_creator_admin_permissions = false
  }

  # Kubernetes Secrets are envelope-encrypted under the cell's storage key.
  # The cell's workloads do not use Kubernetes Secrets for credentials — they
  # resolve references under workload identity — but the cluster itself holds
  # some (service-account signing material, add-on configuration).
  encryption_config {
    resources = ["secrets"]
    provider {
      key_arn = aws_kms_key.storage.arn
    }
  }

  enabled_cluster_log_types = ["api", "audit", "authenticator", "controllerManager", "scheduler"]

  tags = local.tags

  depends_on = [
    aws_iam_role_policy_attachment.cluster,
    aws_cloudwatch_log_group.cluster,
    terraform_data.residency_guard,
  ]
}

# IRSA. The issuer is the cluster's own; a workload identity minted by this
# cluster is meaningless to any other cell's IAM.
resource "aws_iam_openid_connect_provider" "this" {
  url            = local.oidc_issuer
  client_id_list = ["sts.amazonaws.com"]
  tags           = local.tags
}

# --- nodes --------------------------------------------------------------------

data "aws_iam_policy_document" "node_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "node" {
  name               = "${local.eks_name}-node"
  assume_role_policy = data.aws_iam_policy_document.node_assume.json
  tags               = local.tags
}

resource "aws_iam_role_policy_attachment" "node" {
  for_each = toset([
    "AmazonEKSWorkerNodePolicy",
    "AmazonEC2ContainerRegistryReadOnly",
  ])

  role       = aws_iam_role.node.name
  policy_arn = "arn:${local.partition}:iam::aws:policy/${each.value}"
}

# IMDSv2 only, hop limit 1: a pod cannot reach the instance metadata service
# and borrow the node role. Every pod credential comes from its own service
# account's workload identity, or it has none.
resource "aws_launch_template" "node" {
  name_prefix = "${local.eks_name}-node-"

  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
  }

  block_device_mappings {
    device_name = "/dev/xvda"
    ebs {
      volume_size = 50
      volume_type = "gp3"
      encrypted   = true
      kms_key_id  = aws_kms_key.storage.arn
    }
  }

  tag_specifications {
    resource_type = "instance"
    tags          = local.tags
  }

  tags = local.tags
}

# x86_64 because the release pipeline builds single-platform amd64 images.
resource "aws_eks_node_group" "workload" {
  cluster_name    = aws_eks_cluster.this.name
  node_group_name = "workload"
  node_role_arn   = aws_iam_role.node.arn
  subnet_ids      = aws_subnet.workload[*].id
  instance_types  = local.node_instance_types
  ami_type        = "AL2023_x86_64_STANDARD"
  capacity_type   = "ON_DEMAND"

  scaling_config {
    min_size     = var.kubernetes.min_nodes
    desired_size = var.kubernetes.min_nodes
    max_size     = var.kubernetes.max_nodes
  }

  launch_template {
    id      = aws_launch_template.node.id
    version = aws_launch_template.node.latest_version
  }

  # Cilium taints nodes until its agent is ready; nothing schedules onto a node
  # whose network policy is not yet enforced.
  taint {
    key    = "node.cilium.io/agent-not-ready"
    value  = "true"
    effect = "NO_EXECUTE"
  }

  tags = local.tags

  depends_on = [aws_iam_role_policy_attachment.node]

  lifecycle {
    ignore_changes = [scaling_config[0].desired_size]
  }
}
