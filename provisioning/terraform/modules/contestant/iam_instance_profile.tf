resource "aws_iam_instance_profile" "ssm" {
  name_prefix = "${var.project_name}-ssm-"
  role        = aws_iam_role.ssm.name

  tags = var.common_tags
}
