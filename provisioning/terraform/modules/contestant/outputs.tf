output "instances" {
  value = {
    for index, instance in aws_instance.contestant : format("contestant-%02d", index + 1) => {
      instance_id = instance.id
      private_ip  = instance.private_ip
      public_ip   = instance.public_ip
    }
  }
}

output "instance_ids" {
  value = aws_instance.contestant[*].id
}

output "private_ips" {
  value = aws_instance.contestant[*].private_ip
}

output "public_ips" {
  value = {
    for index, instance in aws_instance.contestant : format("contestant-%02d", index + 1) => instance.public_ip
  }
}
