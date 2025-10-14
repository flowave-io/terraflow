variable "some_var" {
  type    = string
  default = "initial"
}

output "some_var_upper" {
  value = upper(var.some_var)
}
