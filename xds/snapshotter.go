package xds

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	clusterservice "github.com/envoyproxy/go-control-plane/envoy/service/cluster/v3"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	endpointservice "github.com/envoyproxy/go-control-plane/envoy/service/endpoint/v3"
	listenerservice "github.com/envoyproxy/go-control-plane/envoy/service/listener/v3"
	routeservice "github.com/envoyproxy/go-control-plane/envoy/service/route/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	cache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	xds "github.com/envoyproxy/go-control-plane/pkg/server/v3"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/utilitywarehouse/semaphore-xds/log"
)

const grpcMaxConcurrentStreams = 1000

// Stream will keep the peer address handy for logging and a rate limiter per stream
type Stream struct {
	peerAddress      string
	requestRateLimit *rate.Limiter
}

type Snapshotter struct {
	servePort              uint
	servicesCache          cache.SnapshotCache
	serviceSnapVersion     int32
	endpointsCache         cache.SnapshotCache
	endpointsSnapVersion   int32
	muxCache               cache.MuxCache
	requestRateLimit       *rate.Limiter     // maximum number of requests allowed to server
	streamRequestPerSecond float64           // maximum number of requests per stream per second
	streams                map[int64]*Stream // map of open streams
	streamsLock            sync.RWMutex
}

// Maps type urls to services and endpoints for muxCache
func mapTypeURL(typeURL string) string {
	switch typeURL {
	case resource.ListenerType, resource.RouteType, resource.ClusterType:
		return "services"
	case resource.EndpointType:
		return "endpoints"
	default:
		return ""
	}
}

// NewSnapshotter needs a grpc server port and the allowed requests limits per server and stream per second
func NewSnapshotter(port uint, requestLimit, streamRequestLimit float64) *Snapshotter {
	servicesCache := cache.NewSnapshotCache(false, cache.IDHash{}, log.EnvoyLogger)
	endpointsCache := cache.NewSnapshotCache(false, cache.IDHash{}, log.EnvoyLogger) // This could be a linear cache? https://pkg.go.dev/github.com/envoyproxy/go-control-plane/pkg/cache/v3#LinearCache
	muxCache := cache.MuxCache{
		Classify: func(r *cache.Request) string {
			return mapTypeURL(r.TypeUrl)
		},
		ClassifyDelta: func(r *cache.DeltaRequest) string {
			return mapTypeURL(r.TypeUrl)
		},
		Caches: map[string]cache.Cache{
			"services":  servicesCache,
			"endpoints": endpointsCache,
		},
	}
	return &Snapshotter{
		servePort:              port,
		servicesCache:          servicesCache,
		endpointsCache:         endpointsCache,
		muxCache:               muxCache,
		requestRateLimit:       rate.NewLimiter(rate.Limit(requestLimit), 1),
		streamRequestPerSecond: streamRequestLimit,
		streams:                make(map[int64]*Stream),
		streamsLock:            sync.RWMutex{},
	}
}

func (s *Snapshotter) ServicesSnapshot(nodeID string) (cache.ResourceSnapshot, error) {
	return s.servicesCache.GetSnapshot(nodeID)
}

func (s *Snapshotter) EndpointsSnapshot(nodeID string) (cache.ResourceSnapshot, error) {
	return s.endpointsCache.GetSnapshot(nodeID)
}

// SnapServices dumps the list of watched Kubernetes Services into services
// snapshot
func (s *Snapshotter) SnapServices(serviceStore XdsServiceStore) error {
	ctx := context.Background()
	cls, rds, lsnr, err := servicesToResources(serviceStore)
	if err != nil {
		return fmt.Errorf("Failed to snapshot Services: %v", err)
	}
	atomic.AddInt32(&s.serviceSnapVersion, 1)
	resources := map[string][]types.Resource{
		resource.ClusterType:  cls,
		resource.ListenerType: lsnr,
		resource.RouteType:    rds,
	}
	snapshot, err := cache.NewSnapshot(fmt.Sprint(s.serviceSnapVersion), resources)
	err = s.servicesCache.SetSnapshot(ctx, EmptyNodeID, snapshot)
	if err != nil {
		return fmt.Errorf("Failed to set services snapshot %v", err)
	}
	return nil
}

// SnapEndpoints dumps the list of watched Kubernetes EndpointSlices into
// endoints snapshot
func (s *Snapshotter) SnapEndpoints(endpointStore XdsEndpointStore) error {
	ctx := context.Background()
	eds, err := endpointSlicesToClusterLoadAssignments(endpointStore)
	if err != nil {
		return fmt.Errorf("Failed to snapshot EndpointSlices: %v", err)
	}
	atomic.AddInt32(&s.endpointsSnapVersion, 1)
	resources := map[string][]types.Resource{
		resource.EndpointType: eds,
	}
	snapshot, err := cache.NewSnapshot(fmt.Sprint(s.endpointsSnapVersion), resources)
	err = s.endpointsCache.SetSnapshot(ctx, EmptyNodeID, snapshot)
	if err != nil {
		return fmt.Errorf("Failed to set endpoints snapshot %v", err)
	}
	return nil
}

func (s *Snapshotter) OnStreamOpen(ctx context.Context, id int64, typ string) error {
	var peerAddr string
	if peerInfo, ok := peer.FromContext(ctx); ok {
		peerAddr = peerInfo.Addr.String()
	}
	log.Logger.Info("OnStreamOpen", "peer address", peerAddr, "id", id, "type", typ)
	s.streamsLock.Lock()
	defer s.streamsLock.Unlock()
	s.streams[id] = &Stream{
		peerAddress:      peerAddr,
		requestRateLimit: rate.NewLimiter(rate.Limit(s.streamRequestPerSecond), 1),
	}
	return nil
}

func (s *Snapshotter) OnStreamClosed(id int64, node *core.Node) {
	log.Logger.Info("OnStreamClosed", "id", id, "node", node)
	s.streamsLock.Lock()
	defer s.streamsLock.Unlock()
	delete(s.streams, id)
}

func (s *Snapshotter) OnStreamRequest(id int64, r *discovery.DiscoveryRequest) error {
	ctx := context.Background()
	s.streamsLock.RLock()
	stream := s.streams[id]
	s.streamsLock.RUnlock()
	log.Logger.Info("OnStreamRequest",
		"id", id,
		"peer", stream.peerAddress,
		"received", r.GetTypeUrl(),
		"node", r.GetNode().GetId(),
		"locality", r.GetNode().GetLocality(),
		"names", strings.Join(r.GetResourceNames(), ", "),
		"version", r.GetVersionInfo(),
	)
	s.debugDiscoveryRequest(r)

	// Verify peer is not exceeding requests rate limit
	if err := waitForRequestLimit(ctx, stream.requestRateLimit); err != nil {
		log.Logger.Warn("Peer: %s exceeded rate limit: %v", stream.peerAddress, err)
		return status.Errorf(codes.ResourceExhausted, "stream request rate limit exceeded: %v", err)
	}
	// Verify server's global request limit
	if err := waitForRequestLimit(ctx, s.requestRateLimit); err != nil {
		log.Logger.Warn("Sever rate limit exceeded: %v for peer request", err, stream.peerAddress)
		return status.Errorf(codes.ResourceExhausted, "stream request rate limit exceeded: %v", err)
	}
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

func (s *Snapshotter) OnDeltaStreamClosed(id int64, node *core.Node) {
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

	xdsServer := xds.NewServer(ctx, &s.muxCache, s)
	grpcOptions := []grpc.ServerOption{
		grpc.MaxConcurrentStreams(grpcMaxConcurrentStreams),
		grpc.KeepaliveParams(keepalive.ServerParameters{MaxConnectionIdle: 30 * time.Minute}),
	}
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

func waitForRequestLimit(ctx context.Context, requestRateLimit *rate.Limiter) error {
	if requestRateLimit.Limit() == 0 {
		// Allow opt out when rate limiting is set to 0qps
		return nil
	}
	// Give a bit of time for queue to clear out, but if not fail after 1 second.
	// Returning an error will cause the stream to be closed. Client will connect
	// to another instance in best case, or retry with backoff.
	wait, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return requestRateLimit.Wait(wait)
}

func (s *Snapshotter) debugDiscoveryRequest(r *discovery.DiscoveryRequest) {
	var nodeSnap cache.ResourceSnapshot
	if mapTypeURL(r.GetTypeUrl()) == "services" {
		nodeSnap, _ = s.servicesCache.GetSnapshot(r.GetNode().GetId())
	}
	if mapTypeURL(r.GetTypeUrl()) == "endpoints" {
		nodeSnap, _ = s.endpointsCache.GetSnapshot(r.GetNode().GetId())
	}
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
