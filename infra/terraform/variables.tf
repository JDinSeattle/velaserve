variable "aws_region" {
  description = "AWS region whose identity, GPU quota, and availability zones were preflighted."
  type        = string
}

variable "cluster_name" {
  description = "Exact EKS cluster name used by every preflight and cleanup guard."
  type        = string
  default     = "velaserve-z0"

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{2,39}$", var.cluster_name))
    error_message = "cluster_name must be a lowercase, explicit EKS name between 3 and 40 characters."
  }
}

variable "kubernetes_version" {
  description = "EKS Kubernetes version confirmed available in the selected region."
  type        = string
  default     = "1.34"
}

variable "vpc_cidr" {
  description = "Dedicated VPC CIDR for the experiment cluster."
  type        = string
  default     = "10.84.0.0/16"
}

variable "availability_zone_count" {
  description = "Number of standard availability zones used for public, private, and control-plane subnets."
  type        = number
  default     = 3

  validation {
    condition     = var.availability_zone_count >= 2 && var.availability_zone_count <= 3
    error_message = "availability_zone_count must be 2 or 3."
  }
}

variable "cluster_endpoint_public_access" {
  description = "Expose the Kubernetes API publicly; false is the secure default."
  type        = bool
  default     = false
}

variable "cluster_endpoint_public_access_cidrs" {
  description = "Operator-controlled CIDRs allowed to reach a public Kubernetes API endpoint."
  type        = list(string)
  default     = []

  validation {
    condition     = !var.cluster_endpoint_public_access || length(var.cluster_endpoint_public_access_cidrs) > 0
    error_message = "A public endpoint requires at least one explicit operator CIDR."
  }
}

variable "cpu_instance_types" {
  description = "Homogeneous CPU node types used only for system and Karpenter controller pods."
  type        = list(string)
  default     = ["m7i.large"]
}

variable "cpu_desired_size" {
  description = "Small always-on CPU system node group size."
  type        = number
  default     = 2
}

variable "gpu_instance_types" {
  description = "Quota-confirmed homogeneous GPU allowlist; g6e.xlarge is the first documented single-L40S choice."
  type        = list(string)
  default     = ["g6e.xlarge"]

  validation {
    condition     = length(var.gpu_instance_types) > 0 && length(var.gpu_instance_types) <= 4
    error_message = "Provide one to four explicitly quota-confirmed GPU instance types."
  }
}

variable "gpu_ami_id" {
  description = "Exact EKS-optimized AL2023 GPU AMI ID for the selected Kubernetes version and architecture."
  type        = string

  validation {
    condition     = can(regex("^ami-[0-9a-f]{8,17}$", var.gpu_ami_id))
    error_message = "gpu_ami_id must be an exact AMI ID, never an alias such as latest."
  }
}

variable "gpu_desired_size" {
  description = "Planning guard only: zero means Karpenter scale-to-zero; a real run must request at least the benchmark minimum through homogeneous pods."
  type        = number
  default     = 0

  validation {
    condition     = var.gpu_desired_size == 0 || (var.gpu_desired_size >= var.gpu_minimum_benchmark_replicas && var.gpu_desired_size <= var.gpu_maximum_replicas)
    error_message = "gpu_desired_size must remain zero or meet the preregistered six-replica minimum without exceeding the cap."
  }
}

variable "gpu_minimum_benchmark_replicas" {
  description = "Preregistered lower bound for a real-GPU evidence run."
  type        = number
  default     = 6
}

variable "gpu_maximum_replicas" {
  description = "Hard experiment cost and cardinality cap."
  type        = number
  default     = 8
}

variable "gpu_capacity_type" {
  description = "Karpenter capacity type. On-demand is the reproducibility default."
  type        = string
  default     = "on-demand"

  validation {
    condition     = contains(["on-demand", "spot"], var.gpu_capacity_type)
    error_message = "gpu_capacity_type must be on-demand or spot."
  }
}

variable "artifact_bucket_name" {
  description = "Globally unique S3 bucket used only for immutable experiment bundles."
  type        = string
}

variable "artifact_retention_days" {
  description = "Days before noncurrent artifact versions expire."
  type        = number
  default     = 365
}

variable "karpenter_chart_version" {
  description = "Pinned Karpenter Helm chart version supported by the pinned EKS module."
  type        = string
  default     = "1.11.3"
}

variable "namespace" {
  description = "Namespace containing VelaServe experiment workloads and the artifact writer service account."
  type        = string
  default     = "velaserve-z0"
}

variable "tags" {
  description = "Additional tags applied to every supported AWS resource."
  type        = map(string)
  default     = {}
}
