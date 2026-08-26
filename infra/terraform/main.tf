data "aws_availability_zones" "available" {
  state = "available"

  filter {
    name   = "opt-in-status"
    values = ["opt-in-not-required"]
  }
}

data "aws_caller_identity" "current" {}

data "aws_ecrpublic_authorization_token" "karpenter" {
  provider = aws.us_east_1
}

locals {
  availability_zones = slice(data.aws_availability_zones.available.names, 0, var.availability_zone_count)
  common_tags = merge(var.tags, {
    Project       = "VelaServe"
    Stage         = "z0"
    ManagedBy     = "Terraform"
    EvidenceScope = "real-gpu-pending"
  })
}

module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "6.6.1"

  name = var.cluster_name
  cidr = var.vpc_cidr
  azs  = local.availability_zones

  private_subnets = [for index, _ in local.availability_zones : cidrsubnet(var.vpc_cidr, 4, index)]
  public_subnets  = [for index, _ in local.availability_zones : cidrsubnet(var.vpc_cidr, 8, index + 48)]
  intra_subnets   = [for index, _ in local.availability_zones : cidrsubnet(var.vpc_cidr, 8, index + 52)]

  enable_nat_gateway = true
  single_nat_gateway = true

  public_subnet_tags = {
    "kubernetes.io/role/elb" = "1"
  }
  private_subnet_tags = {
    "kubernetes.io/role/internal-elb" = "1"
    "karpenter.sh/discovery"          = var.cluster_name
  }

  tags = local.common_tags
}

module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "21.24.2"

  name               = var.cluster_name
  kubernetes_version = var.kubernetes_version

  endpoint_public_access       = var.cluster_endpoint_public_access
  endpoint_public_access_cidrs = var.cluster_endpoint_public_access_cidrs
  endpoint_private_access      = true

  enable_cluster_creator_admin_permissions = true
  enabled_log_types = [
    "api",
    "audit",
    "authenticator",
    "controllerManager",
    "scheduler",
  ]

  compute_config = {
    enabled = false
  }

  addons = {
    coredns                = {}
    eks-pod-identity-agent = { before_compute = true }
    kube-proxy             = {}
    vpc-cni                = { before_compute = true }
  }

  vpc_id                   = module.vpc.vpc_id
  subnet_ids               = module.vpc.private_subnets
  control_plane_subnet_ids = module.vpc.intra_subnets

  eks_managed_node_groups = {
    system = {
      ami_type       = "AL2023_x86_64_STANDARD"
      instance_types = var.cpu_instance_types
      capacity_type  = "ON_DEMAND"
      min_size       = 2
      max_size       = 3
      desired_size   = var.cpu_desired_size
      disk_size      = 80

      labels = {
        "karpenter.sh/controller" = "true"
        "velaserve.dev/role"      = "system"
      }
    }
  }

  node_security_group_tags = {
    "karpenter.sh/discovery" = var.cluster_name
  }

  tags = local.common_tags
}

module "karpenter" {
  source  = "terraform-aws-modules/eks/aws//modules/karpenter"
  version = "21.24.2"

  cluster_name                    = module.eks.cluster_name
  namespace                       = "kube-system"
  create_pod_identity_association = true
  node_iam_role_use_name_prefix   = false
  node_iam_role_name              = "${var.cluster_name}-karpenter-node"
  queue_name                      = "${var.cluster_name}-karpenter"

  node_iam_role_additional_policies = {
    AmazonSSMManagedInstanceCore = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
  }

  tags = local.common_tags
}

resource "helm_release" "karpenter" {
  name       = "karpenter"
  namespace  = "kube-system"
  repository = "oci://public.ecr.aws/karpenter"
  chart      = "karpenter"
  version    = var.karpenter_chart_version

  repository_username = data.aws_ecrpublic_authorization_token.karpenter.user_name
  repository_password = data.aws_ecrpublic_authorization_token.karpenter.password

  atomic  = true
  wait    = true
  timeout = 600

  values = [yamlencode({
    nodeSelector = {
      "karpenter.sh/controller" = "true"
    }
    settings = {
      clusterName       = module.eks.cluster_name
      clusterEndpoint   = module.eks.cluster_endpoint
      interruptionQueue = module.karpenter.queue_name
    }
    webhook = {
      enabled = false
    }
  })]

  depends_on = [module.eks, module.karpenter]
}

resource "aws_security_group" "gpu_p2p" {
  name        = "${var.cluster_name}-gpu-p2p"
  description = "VelaServe Z0 homogeneous GPU peer traffic; attached alongside the EKS node security group"
  vpc_id      = module.vpc.vpc_id

  tags = merge(local.common_tags, {
    Name = "${var.cluster_name}-gpu-p2p"
  })
}

resource "aws_vpc_security_group_ingress_rule" "gpu_p2p_self" {
  security_group_id            = aws_security_group.gpu_p2p.id
  referenced_security_group_id = aws_security_group.gpu_p2p.id
  ip_protocol                  = "-1"
  description                  = "All transport between the preregistered homogeneous GPU peers"
}

resource "aws_vpc_security_group_egress_rule" "gpu_p2p_ipv4" {
  security_group_id = aws_security_group.gpu_p2p.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
  description       = "Outbound model, image, metrics, and AWS API access through the VPC NAT gateway"
}

resource "aws_ecr_repository" "velaserve" {
  name                 = "velaserve"
  image_tag_mutability = "IMMUTABLE"
  force_delete         = false

  encryption_configuration {
    encryption_type = "AES256"
  }

  image_scanning_configuration {
    scan_on_push = true
  }
}

resource "aws_ecr_repository" "llm_d_router" {
  name                 = "velaserve-llm-d-router"
  image_tag_mutability = "IMMUTABLE"
  force_delete         = false

  encryption_configuration {
    encryption_type = "AES256"
  }

  image_scanning_configuration {
    scan_on_push = true
  }
}

resource "aws_ecr_lifecycle_policy" "repositories" {
  for_each   = toset([aws_ecr_repository.velaserve.name, aws_ecr_repository.llm_d_router.name])
  repository = each.value
  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Retain the newest 30 immutable experiment images"
      selection = {
        tagStatus   = "any"
        countType   = "imageCountMoreThan"
        countNumber = 30
      }
      action = { type = "expire" }
    }]
  })
}

resource "aws_s3_bucket" "artifacts" {
  bucket        = var.artifact_bucket_name
  force_destroy = false
}

resource "aws_s3_bucket_public_access_block" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_versioning" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

data "aws_iam_policy_document" "artifacts_bucket" {
  statement {
    sid     = "DenyInsecureTransport"
    effect  = "Deny"
    actions = ["s3:*"]
    resources = [
      aws_s3_bucket.artifacts.arn,
      "${aws_s3_bucket.artifacts.arn}/*",
    ]
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
}

resource "aws_s3_bucket_policy" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id
  policy = data.aws_iam_policy_document.artifacts_bucket.json
}

resource "aws_s3_bucket_lifecycle_configuration" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id

  rule {
    id     = "expire-noncurrent-versions"
    status = "Enabled"

    filter {}

    noncurrent_version_expiration {
      noncurrent_days = var.artifact_retention_days
    }
  }

  depends_on = [aws_s3_bucket_versioning.artifacts]
}

data "aws_iam_policy_document" "artifact_writer_assume" {
  statement {
    actions = ["sts:AssumeRole", "sts:TagSession"]
    principals {
      type        = "Service"
      identifiers = ["pods.eks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "artifact_writer" {
  name               = "${var.cluster_name}-artifact-writer"
  assume_role_policy = data.aws_iam_policy_document.artifact_writer_assume.json
}

data "aws_iam_policy_document" "artifact_writer" {
  statement {
    sid       = "ListExperimentPrefixes"
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.artifacts.arn]
    condition {
      test     = "StringLike"
      variable = "s3:prefix"
      values   = ["runs/*"]
    }
  }

  statement {
    sid       = "WriteAndVerifyExperimentObjects"
    actions   = ["s3:GetObject", "s3:GetObjectVersion", "s3:PutObject"]
    resources = ["${aws_s3_bucket.artifacts.arn}/runs/*"]
  }
}

resource "aws_iam_role_policy" "artifact_writer" {
  name   = "artifacts"
  role   = aws_iam_role.artifact_writer.id
  policy = data.aws_iam_policy_document.artifact_writer.json
}

resource "kubernetes_namespace_v1" "velaserve" {
  metadata {
    name = var.namespace
    labels = {
      "app.kubernetes.io/part-of"    = "velaserve"
      "velaserve.dev/evidence-scope" = "real-gpu-pending"
    }
  }

  depends_on = [module.eks]
}

resource "kubernetes_service_account_v1" "artifact_writer" {
  metadata {
    name      = "velaserve-artifact-writer"
    namespace = kubernetes_namespace_v1.velaserve.metadata[0].name
  }
  automount_service_account_token = false
}

resource "aws_eks_pod_identity_association" "artifact_writer" {
  cluster_name    = module.eks.cluster_name
  namespace       = kubernetes_namespace_v1.velaserve.metadata[0].name
  service_account = kubernetes_service_account_v1.artifact_writer.metadata[0].name
  role_arn        = aws_iam_role.artifact_writer.arn
}
