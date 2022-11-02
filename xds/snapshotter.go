package xds

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync/atomic"

	clusterservice "github.com/envoyproxy/go-control-plane/envoy/service/cluster/v3"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	endpointservice "github.com/envoyproxy/go-control-plane/envoy/service/endpoint/v3"
	listenerservice "github.com/envoyproxy/go-control-plane/envoy/service/listener/v3"
	routeservice "github.com/envoyproxy/go-control-plane/envoy/service/route/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	cache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	xds "github.com/envoyproxy/go-control-plane/pkg/server/v3"
	"google.golang.org/grpc"

	"github.com/utilitywarehouse/semaphore-xds/kube"
	"github.com/utilitywarehouse/semaphore-xds/log"
)

const grpcMaxConcurrentStreams = 1000

type Snapshotter struct {
	version int32

	servePort uint
	snapshot  *cache.Snapshot
	Cache     cache.SnapshotCache // Maybe we could use a muxCache here and split services and endpoints to save some compute
}

func NewSnapshotter(port uint) *Snapshotter {
	return &Snapshotter{
		servePort: port,
		Cache:     cache.NewSnapshotCache(false, cache.IDHash{}, log.EnvoyLogger),
	}
}

// Snap throws the list of the passed Service and EndpointSlice watchers
// resources into a snapshot
func (s *Snapshotter) Snap(sw *kube.ServiceWatcher, ew *kube.EndpointSliceWatcher) error {
	ctx := context.Background()
	cls, rds, lsnr, err := servicesToResources(sw)
	if err != nil {
		return fmt.Errorf("Failed to snapshot Services: %v", err)
	}
	eds, err := endpointSlicesToClusterLoadAssignments(ew)
	if err != nil {
		return fmt.Errorf("Failed to snapshot EndpointSlices: %v", err)
	}

	atomic.AddInt32(&s.version, 1)
	nodeID := "" // Dummy empty node ID
	// Refactor to something like:
	// https://github.com/wongnai/xds/blob/5e94e78816be973880f73e947aa4306610b100a6/snapshot/resource.go
	// see if we can save some work by snapshotting services and endpointslices separately
	resources := map[string][]types.Resource{
		resource.EndpointType: eds,
		resource.ClusterType:  cls,
		resource.ListenerType: lsnr,
		resource.RouteType:    rds,
	}
	s.snapshot, err = cache.NewSnapshot(fmt.Sprint(s.version), resources)
	err = s.Cache.SetSnapshot(ctx, nodeID, s.snapshot)
	if err != nil {
		return fmt.Errorf("Failed to set snapshot %v", err)
	}

	return nil
}

func (s *Snapshotter) OnStreamOpen(ctx context.Context, id int64, typ string) error {
	log.Logger.Info("OnStreamOpen", "id", id, "type", typ)
	return nil
}

func (s *Snapshotter) OnStreamClosed(id int64) {
	log.Logger.Info("OnStreamClosed", "id", id)
}

func (s *Snapshotter) OnStreamRequest(id int64, r *discovery.DiscoveryRequest) error {
	log.Logger.Info("OnStreamRequest",
		"id", id,
		"received", r.GetTypeUrl(),
		"node", r.GetNode().GetId(),
		"locality", r.GetNode().GetLocality(),
		"names", strings.Join(r.GetResourceNames(), ", "),
		"version", r.GetVersionInfo(),
	)
	s.debugDiscoveryRequest(r)
	return nil
}
func (s *Snapshotter) OnStreamResponse(ctx context.Context, id int64, req *discovery.DiscoveryRequest, resp *discovery.DiscoveryResponse) {
	log.Logger.Info("OnStreamResponse",
		"id", id,
		"type", resp.GetTypeUrl(),
		"version", resp.GetVersionInfo(),
		"resources", len(resp.GetResources()),
	)
}

func (s *Snapshotter) OnFetchRequest(ctx context.Context, req *discovery.DiscoveryRequest) error {
	log.Logger.Info("OnFetchRequest")
	return nil
}

func (s *Snapshotter) OnDeltaStreamClosed(id int64) {
	log.Logger.Info("OnDeltaStreamClosed")
}

func (s *Snapshotter) OnDeltaStreamOpen(ctx context.Context, id int64, typ string) error {
	log.Logger.Info("OnDeltaStreamOpen")
	return nil
}

func (s *Snapshotter) OnStreamDeltaRequest(i int64, request *discovery.DeltaDiscoveryRequest) error {
	log.Logger.Info("OnStreamDeltaRequest")
	return nil
}

func (s *Snapshotter) OnStreamDeltaResponse(i int64, request *discovery.DeltaDiscoveryRequest, response *discovery.DeltaDiscoveryResponse) {
	log.Logger.Info("OnStreamDeltaResponse")
}

func (s *Snapshotter) OnFetchResponse(req *discovery.DiscoveryRequest, resp *discovery.DiscoveryResponse) {
	log.Logger.Info("OnFetchResponse")
}

// ListenAndServeFromCache will start an xDS server at the given port and serve
// snapshots from the given cache
func (s *Snapshotter) ListenAndServe() {
	ctx := context.Background()

	xdsServer := xds.NewServer(ctx, s.Cache, s)
	grpcOptions := []grpc.ServerOption{grpc.MaxConcurrentStreams(grpcMaxConcurrentStreams)}
	grpcServer := grpc.NewServer(grpcOptions...)
	registerServices(grpcServer, xdsServer)
	runGrpcServer(ctx, grpcServer, s.servePort)
}

// registerServices registers xds services served by our grpc server
func registerServices(grpcServer *grpc.Server, xdsServer xds.Server) {
	discovery.RegisterAggregatedDiscoveryServiceServer(grpcServer, xdsServer)
	endpointservice.RegisterEndpointDiscoveryServiceServer(grpcServer, xdsServer)
	clusterservice.RegisterClusterDiscoveryServiceServer(grpcServer, xdsServer)
	routeservice.RegisterRouteDiscoveryServiceServer(grpcServer, xdsServer)
	listenerservice.RegisterListenerDiscoveryServiceServer(grpcServer, xdsServer)
}

// runGrpcServer starts the passed grpc server at the given port.
func runGrpcServer(ctx context.Context, grpcServer *grpc.Server, port uint) {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Logger.Error("Failed to start grpc server", "port", port, "error", err)
		return
	}
	log.Logger.Info("Management GRPC server listening", "port", port)
	go func() {
		if err = grpcServer.Serve(lis); err != nil {
			log.Logger.Error("failed to serve")
		}
	}()
	<-ctx.Done()
	grpcServer.GracefulStop()
}

func (s *Snapshotter) debugDiscoveryRequest(r *discovery.DiscoveryRequest) {
	nodeSnap, _ := s.Cache.GetSnapshot(r.GetNode().GetId())
	if nodeSnap == nil {
		return
	}
	nodeSnapResources := nodeSnap.GetResourcesAndTTL(r.TypeUrl)
	log.Logger.Debug("Complete node snapshot",
		"resources", nodeSnapResources,
		"length(resources)", len(nodeSnapResources),
	)
	for _, name := range r.GetResourceNames() {
		if res, exists := nodeSnapResources[name]; exists {
			log.Logger.Debug("Requested Resource Found", "name", name, "resource", res)
		} else {
			log.Logger.Debug("Could not find resource", "name", name)
			// Calculate and print a list of available resource keys to use
			names := make([]string, len(nodeSnapResources))
			i := 0
			for n := range nodeSnapResources {
				names[i] = n
				i++
			}
			log.Logger.Debug("Available resources list", "names", names)
		}
	}

}
