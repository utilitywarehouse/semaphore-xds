# Tooling to deploy envoy as a sidecar and use it with the xDS server

This is in a very experimental stage and definitely not production ready.

It includes a small app to be used as an initContainer to produce envoy config
based on a map of xDS services and local ports and a kustomize base to deploy a
Kyverno mutating rule for the initContainer and the envoy sidecar.
