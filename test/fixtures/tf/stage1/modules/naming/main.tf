variable "prefix" {
  type = string
}

resource "random_id" "suffix" {
  byte_length = 4
  prefix      = "${var.prefix}-"
}

output "name" {
  value = random_id.suffix.hex
}
