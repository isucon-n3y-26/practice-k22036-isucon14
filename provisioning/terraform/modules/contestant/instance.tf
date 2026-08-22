resource "aws_instance" "contestant" {
  count = var.instance_count

  ami                         = data.aws_ssm_parameter.ubuntu_2404_amd64.value
  instance_type               = var.instance_type
  subnet_id                   = var.subnet_id
  associate_public_ip_address = true
  vpc_security_group_ids      = [var.security_group_id]
  iam_instance_profile        = aws_iam_instance_profile.ssm.name
  key_name                    = local.key_name

  root_block_device {
    volume_type           = "gp3"
    volume_size           = var.volume_size
    encrypted             = true
    delete_on_termination = true
  }

  metadata_options {
    http_endpoint = "enabled"
    http_tokens   = "required"
  }

  tags = merge(var.common_tags, {
    Name = format("%s-contestant-%02d", var.project_name, count.index + 1)
    Role = "contestant"
  })
}
