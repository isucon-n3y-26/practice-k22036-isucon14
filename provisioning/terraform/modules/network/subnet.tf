resource "aws_subnet" "public" {
  vpc_id                  = aws_vpc.main.id
  cidr_block              = var.public_subnet_cidr
  availability_zone       = local.availability_zone
  map_public_ip_on_launch = true

  tags = merge(var.common_tags, {
    Name = "${var.project_name}-public"
  })
}
