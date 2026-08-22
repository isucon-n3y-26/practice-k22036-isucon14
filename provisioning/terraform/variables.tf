variable "aws_profile" {
  description = "Optional AWS CLI profile used by the provider."
  type        = string
  default     = null
  nullable    = true
}

variable "key_name" {
  description = "Optional existing EC2 key pair name. When null, ssh_public_key is registered as a managed key pair."
  type        = string
  default     = null
  nullable    = true
}

variable "ssh_public_key" {
  description = "Optional SSH public key registered in EC2 when key_name is null. The setup script supplies this automatically."
  type        = string
  default     = null
  nullable    = true
}

variable "allowed_ssh_cidrs" {
  description = "CIDRs allowed to connect over SSH. Empty by default; use SSM Session Manager instead."
  type        = list(string)
  default     = []
}

variable "allowed_https_cidrs" {
  description = "Public CIDRs allowed to access contestant HTTPS. The benchmarker is always allowed privately."
  type        = list(string)
  default     = []
}



