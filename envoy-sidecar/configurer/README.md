# semaphore-xds-envoy-configurer

Expects an environment variable named `ENVOY_SIDECAR_TARGETS` in the form of a
comma separated list of xDS listeners. For example:

`ENVOY_SIDECAR_TARGETS="<xds-address1>,<xds-address2>"`
`XDS_SERVER_ADDRESS`="semaphore-xds.sys-semaphore.svc.cluster.local"
`XDS_SERVER_PORT`="18000"

It generates envoy config to point listeners on the specified local ports to
the respective xDS dynamic resources in order for envoy to be able to proxy
traffic to the configured services.
