variable "project_name" {
  type = string
}

variable "aws_region" {
  type = string
}

variable "target_private_ips" {
  type = list(string)
}

variable "cpu" {
  type = number
}

variable "memory" {
  type = number
}

variable "image_tag" {
  type = string
}

variable "load_timeout" {
  type = number
}

variable "log_retention_days" {
  type = number
}

variable "common_tags" {
  type    = map(string)
  default = {}
}
