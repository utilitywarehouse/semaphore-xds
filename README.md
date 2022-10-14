# Semaphore-xDS

A very simple xDS server.

It watches Kubernetes Services and the respective EndpointSlices and serves them
via an xDS server to clients.

Currently the code is in super early development stage and should be considered
alpha quality.

# Why Envoy is involved in this (envoy modules to implement the needed resources and APIs)

xDS is the protocol initially used by Envoy, that is evolving into a universal data plan API for service mesh.

https://github.com/grpc/grpc-go/blob/master/examples/features/xds/README.md

# How to use - Server side

Deploy the server [manifests](./deploy/kustomize) in your cluster. The server
needs a [ClusterRole](./deploy/kustomize/cluster/rbac.yaml) to allow it to see
all Services and EndpointSlices in the cluster, and a namespaced [deployment](
./deploy/kustomize/namespaced/).

# How to use - Client side

The user's grpc client needs to specify a config map in json config to point to
the xDS server, like:
```
{
  "xds_servers": [
    {
      "server_uri": "semaphore-xds.sys-semaphore.svc.cluster.local:18000",
      "channel_creds": [
        {
          "type": "insecure"
        }
      ],
      "server_features": ["xds_v3"]          
    }
  ]
}
```
and expose the location as `GRPC_XDS_BOOTSTRAP` env var.

Then you need to import the following module in the client code:
`_ "google.golang.org/grpc/xds"` and call the call xds server addresses.
The expected server address will follow the pattern:
`xds:///<service-name>.<namespace>:<port>`.

For example `xds:///grpc-echo-server.labs:50051`
Careful that this is not a DNS name, so we cannot append a domain there!


