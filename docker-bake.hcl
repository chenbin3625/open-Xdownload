group "default" { targets = ["x-archive"] }
target "x-archive" {
  context = "."
  dockerfile = "Dockerfile"
  platforms = ["linux/amd64", "linux/arm64"]
  tags = ["x-archive:local"]
}
