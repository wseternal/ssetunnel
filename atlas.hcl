env "local" {
  src = "file://schema.hcl"
  dev = "docker://postgres/17/dev"

  migration {
    dir = "file://migrations"
  }
}
