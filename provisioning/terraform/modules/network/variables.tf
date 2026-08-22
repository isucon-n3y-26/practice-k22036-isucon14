variable "project_name" {
  type = string
}

variable "availability_zone" {
  type     = string
  default  = null
  nullable = true
}

variable "vpc_cidr" {
  type = string
}

variable "public_subnet_cidr" {
  type = string
}

variable "allowed_ssh_cidrs" {
  type = list(string)
}

variable "allowed_https_cidrs" {
  type = list(string)
}

variable "common_tags" {
  type    = map(string)
  default = {}
}
