output "vpc_id" {
  value = aws_vpc.main.id
}

output "public_subnet_id" {
  value = aws_subnet.public.id
}

output "contestant_security_group_id" {
  value = aws_security_group.contestant.id
}

output "benchmarker_security_group_id" {
  value = aws_security_group.benchmarker.id
}
