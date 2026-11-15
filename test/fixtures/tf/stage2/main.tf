# Stage 2: the change planned against stage 1's state. Only the null, random and
# local providers are used, so generating the fixtures needs no cloud account.
terraform {
  required_providers {
    random = { source = "hashicorp/random", version = "3.6.3" }
    null   = { source = "hashicorp/null", version = "3.2.3" }
    local  = { source = "hashicorp/local", version = "2.5.2" }
  }
}

variable "environment" {
  type    = string
  default = "staging"
}

resource "random_pet" "server" {
  length = 2
  keepers = {
    image = "ubuntu-24.04"
  }
}

resource "random_password" "db" {
  length  = 20
  special = false
}

resource "random_integer" "port" {
  count = 2
  min   = 8000
  max   = 8999
}

resource "random_string" "api_key" {
  length  = 16
  special = false
}

resource "terraform_data" "config" {
  input = {
    environment = var.environment
    replicas    = 3
  }
}

resource "local_file" "inventory" {
  filename = "${path.module}/out/inventory.txt"
  content  = "server=${random_pet.server.id}\n"
}

module "naming" {
  source = "./modules/naming"
  prefix = "app"
}

output "server_name" {
  value = random_pet.server.id
}

output "db_password" {
  value     = random_password.db.result
  sensitive = true
}

output "ports" {
  value = random_integer.port[*].result
}

output "naming" {
  value = module.naming.name
}
