resource "aws_security_group" "contestant" {
  name_prefix = "${var.project_name}-contestant-"
  description = "Access to the ISUCON14 contestant servers"
  vpc_id      = aws_vpc.main.id

  ingress {
    description     = "HTTPS from benchmarker"
    protocol        = "tcp"
    from_port       = 443
    to_port         = 443
    security_groups = [aws_security_group.benchmarker.id]
  }

  dynamic "ingress" {
    for_each = toset(var.allowed_ssh_cidrs)
    content {
      description = "SSH"
      protocol    = "tcp"
      from_port   = 22
      to_port     = 22
      cidr_blocks = [ingress.value]
    }
  }

  dynamic "ingress" {
    for_each = toset(var.allowed_https_cidrs)
    content {
      description = "Public HTTPS"
      protocol    = "tcp"
      from_port   = 443
      to_port     = 443
      cidr_blocks = [ingress.value]
    }
  }

  egress {
    description = "All outbound traffic"
    protocol    = "-1"
    from_port   = 0
    to_port     = 0
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = merge(var.common_tags, {
    Name = "${var.project_name}-contestant"
  })

  lifecycle {
    create_before_destroy = true
  }
}
