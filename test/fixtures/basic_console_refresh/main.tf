variable "some_var" {
  type    = string
  default = "initial"
}

output "some_var_upper" {
  value = upper(var.some_var)
}

terraform {
  required_providers {
    null = {
      source  = "hashicorp/null"
      version = "~> 3.2"
    }
    time = {
      source  = "hashicorp/time"
      version = "~> 0.9"
    }
  }
}

provider "null" {}
provider "time" {}

# Simulate slow plan using 50 dummy resources
locals {
  count = 2000
}

# Dummy time_sleep resources
resource "time_sleep" "waits" {
  count           = local.count
  create_duration = "5s"
}

# Dummy null resources that depend on waits
resource "null_resource" "dummy" {
  count = local.count

  provisioner "local-exec" {
    command = "echo Resource ${count.index} complete"
  }

  depends_on = [time_sleep.waits]
}
