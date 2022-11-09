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

# Metrics

There are separate metrics available that one can use to determine the status
of the controller. The available metrics can give a visibility on errors from
the Kubernetes clients, the watchers and the controller's queues. In addition to
these, there are metrics available to provide visibility over the resources
stored in the snapshot.

## Kubernetes Client Metrics

- `semaphore_xds_kube_http_request_total`: Total number of HTTP requests to the
  Kubernetes API by host, code and method.
- `semaphore_xds_kube_http_request_duration_seconds`: Histogram of latencies for
  HTTP requests to the Kubernetes API by host and method

## Kubernetes Watcher Metrics

- `semaphore_xds_kube_watcher_objects`: Number of objects watched by kind
- `semaphore_xds_kube_watcher_events_total`: Number of events handled by kind
  and event_type

## Queue Metrics

- `semaphore_xds_queue_depth`: Workqueue depth, by queue name.
- `semaphore_xds_queue_adds_total`: Workqueue adds, by queue name.
- `semaphore_xds_queue_latency_duration_seconds`: Workqueue latency, by queue
  name.
- `semaphore_xds_queue_work_duration_seconds`: Workqueue work duration, by queue
  name.
- `semaphore_xds_queue_unfinished_work_seconds`: Unfinished work in seconds, by
  queue name.
- `semaphore_xds_queue_longest_running_processor_seconds`: Longest running
  processor, by queue name.
- `semaphore_xds_queue_retries_total`: Workqueue retries, by queue name.
- `semaphore_xds_queue_requeued_items`: Items that have been requeued but not
  reconciled yet, by queue name.

## Snapshot Metrics

- `semaphore_xds_snapshot_cluster`: xDS cluster info by name, lb policy and
  discovery type.
- `semaphore_xds_snapshot_listener`: xDS listener info by name and target route.
- `semaphore_xds_snapshot_endpoint`: xDS cluster load assignment endpoints info
  by cluster, locality zone and subzone, address and health.
- `semaphore_xds_snapshot_route`: xDS route configuration info by name, path,
  target domains, virtual host and target cluster.
