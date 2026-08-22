resource "aws_vpc_security_group_ingress_rule" "payment_from_contestants" {
  security_group_id            = aws_security_group.benchmarker.id
  referenced_security_group_id = aws_security_group.contestant.id
  description                  = "Payment mock server from contestants"
  ip_protocol                  = "tcp"
  from_port                    = 12345
  to_port                      = 12345
}
