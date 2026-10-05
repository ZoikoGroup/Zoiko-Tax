output "primary_region" {
  description = "The region the plane accepts writes in."
  value       = data.aws_region.current.region
}

output "tables" {
  description = "Plane table names by purpose."
  value       = { for k, t in aws_dynamodb_table.this : k => t.name }
}

output "key" {
  description = "The plane key identifier (multi-Region primary). Never used for cell data."
  value       = aws_kms_key.plane.arn
}
