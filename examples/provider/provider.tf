provider "tsuru" {
  default_metadata {
    annotations = {
      "terraform.tsuru.io/managed-by" = "terraform"
      "terraform.tsuru.io/source"     = "github.com/org/infra//apps/payments"
    }
  }
}
