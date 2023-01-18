# Semaphore-xDS

A very simple xDS server.

It watches Kubernetes Services and the respective EndpointSlices and serves them
via an xDS server to clients.

Currently the code is in super early development stage and should be considered
alpha quality.

# How to use

## Server side

### Deployment

There are 2 kustomize bases to deploy the clustered and namespaced scoped
resources:
- [Clustered resources](./deploy/kustomize/cluster/) contain RBAC and CRD
  definitions needed by the server to be able to watch Service, EndpointSlice
  and XdsService resources in the ckuster.
- [Namespaced resources](./deploy/kustomize/namespaced/) contain the manifests
  to deploy the xds server.

### Configuration - XdsService

In order to configure which services should be streamed as xds targets to
clients by the server, users need to specify XdsService resources:
```
apiVersion: semaphore-xds.uw.systems/v1alpha1
kind: XdsService
metadata:
  name: foo
spec:
  service:
    name: <service-name> # clients will access the service at: xds:///<service-name>.<namespace>:<port>
  loadBalancing:
    policy: <policy-name>
```

### Configuration - Service labels (legacy - to be removed in the future)

Users can use the following labels on Service resources to instruct the xds
server to stream the target:
- `xds.semaphore.uw.systems/enabled: "true"`: to enable xds load balancing
- `xds.semaphore.uw.systems/lb-policy: "<policy>"`: to specify the load
  balancing policy

If both are specified, configuration that comes from `XdsService` resources
should be preferred than `Service` labels

### Load Balancing Policies

The supported values are derived from the envoy proxy library for [cluster lb
policies](https://pkg.go.dev/github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3#Cluster_LbPolicy).
If none is set, or an invalid value is passed, the server configuration will
default to round robin.

### xDS service address

The expected server address will follow the pattern:
`xds:///<service-name>.<namespace>:<port>`.

For example `xds:///grpc-echo-server.labs:50051`
Careful that this is not a DNS name, so we cannot append a domain there!

## Client side

### Bootstrap Config

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
and expose the location as `GRPC_XDS_BOOTSTRAP` env var. Alternatively, one can
pass the json content as string in `GRPC_XDS_BOOTSTRAP_CONFIG` environment
variable.

Then you need to import the following module in the client code:
`_ "google.golang.org/grpc/xds"` and call the call xds server addresses.

### Mutate Bootstrap Config - Kyverno

The client above configuration should be identical for all clients living in the
same cluster, assuming only one xDS server is deployed. In such case, it is
handy to use a mutation hook to inject the needed environment variable to your
client pods. A Kustomize base to achieve that using Kyverno is provided [here](
./deploy/kustomize/kyverno/mutate/). This is assuming semaphore-xds is deployed
under a namespace called `sys-semaphore` so patch if needed. Your pods will need
to be labeled with `xds.semaphore.uw.systems/client: "true"` in order to be
selected by the mutating rule.

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
