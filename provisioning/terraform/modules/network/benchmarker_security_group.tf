resource "aws_security_group" "benchmarker" {
  name_prefix = "${var.project_name}-benchmarker-"
  description = "Network access for the ISUCON14 benchmarker Fargate tasks"
  vpc_id      = aws_vpc.main.id

  egress {
    description = "All outbound traffic"
    protocol    = "-1"
    from_port   = 0
    to_port     = 0
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = merge(var.common_tags, {
    Name = "${var.project_name}-benchmarker"
  })

  lifecycle {
    create_before_destroy = true
  }
}
