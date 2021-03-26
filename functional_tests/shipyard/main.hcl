variable "consul_k8s_cluster" {
  default = "dc1"
}

variable "consul_k8s_network" {
  default = "dc1"
}

k8s_cluster "dc1" {
  driver  = "k3s"

  nodes = 1

  network {
    name = "network.dc1"
  }
}

variable "consul_enable_tls" {
  default = true
}

variable "consul_enable_acls" {
  default = true
}

network "dc1" {
  subnet = "10.5.0.0/16"
}

module "consul" {
  source = "github.com/shipyard-run/blueprints/modules//kubernetes-consul"
}

output "KUBECONFIG" {
  value = k8s_config("dc1")
}