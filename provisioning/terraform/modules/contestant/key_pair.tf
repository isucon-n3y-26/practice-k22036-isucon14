resource "aws_key_pair" "generated" {
  count = var.key_name == null && var.ssh_public_key != null ? 1 : 0

  key_name_prefix = "${var.project_name}-"
  public_key      = var.ssh_public_key

  tags = merge(var.common_tags, {
    Name = "${var.project_name}-generated"
  })
}

locals {
  key_name = var.key_name != null ? var.key_name : try(aws_key_pair.generated[0].key_name, null)
}
