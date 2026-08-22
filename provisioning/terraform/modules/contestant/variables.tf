variable "project_name" {
  type = string
}

variable "instance_count" {
  type = number
}


variable "instance_type" {
  type = string
}

variable "volume_size" {
  type = number
}

variable "subnet_id" {
  type = string
}

variable "security_group_id" {
  type = string
}

variable "key_name" {
  type     = string
  default  = null
  nullable = true
}

variable "ssh_public_key" {
  description = "Public key used to create an EC2 key pair when key_name is null."
  type        = string
  default     = null
  nullable    = true
}

variable "common_tags" {
  type    = map(string)
  default = {}
}
