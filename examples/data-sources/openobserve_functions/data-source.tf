data "openobserve_functions" "all" {}

output "function_names" {
  value = [for f in data.openobserve_functions.all.functions : f.name]
}

output "javascript_functions" {
  value = [for f in data.openobserve_functions.all.functions : f.name if f.language == "js"]
}
