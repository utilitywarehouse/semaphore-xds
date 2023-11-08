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

const (
	grpcMaxConcurrentStreams = 1000
	// We use EmptyNodeID to create snapshots of kubernetes resources when there
	// are no nodes registered with the server. This will allow us to have a ready
	// to serve snapshot on new node requests and initialise new snapshots just by
	// copying the EmptyNodeID. Also, we can use it when exporting metrics to
	// reduce the amount of series and still expose metrics regarding the server's
	// snapshot resources.
	EmptyNodeID = ""
)

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
	nodes                  Nodes // maps all clients node ids to requested resources that will be snapshotted and served
	nodesLock              sync.RWMutex
}

// Nodes maps a node id to resources to be snapped
type Nodes map[string]*NodeSnapshotResources

// NodeSnapshot keeps resources and versions to help snapshotting per node
type NodeSnapshotResources struct {
	serviceResources        map[string][]types.Resource
	serviceResourcesNames   map[string][]string
	serviceSnapVersion      int32
	endpointsResources      map[string][]types.Resource
	endpointsResourcesNames map[string][]string
	endpointsSnapVersion    int32
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
		nodes:                  make(Nodes),
		nodesLock:              sync.RWMutex{},
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
	for nodeID, node := range s.nodes {
		for typeURL, resources := range node.serviceResourcesNames {
			if err := s.updateNodeServiceSnapshotResources(nodeID, typeURL, resources); err != nil {
				log.Logger.Error("Failed to update service resources before snapping", "type", typeURL, "node", nodeID, "resources", resources, "error", err)
			}
		}
		if err := s.nodeServiceSnapshot(nodeID); err != nil {
			log.Logger.Error("Failed to update service snapshot for node", "node", nodeID, "error", err)
		}
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
	for nodeID, node := range s.nodes {
		for typeURL, resources := range node.endpointsResourcesNames {
			if err := s.updateNodeEndpointsSnapshotResources(nodeID, typeURL, resources); err != nil {
				log.Logger.Error("Failed to update endpoints resources before snapping", "type", typeURL, "node", nodeID, "resources", resources, "error", err)
			}
		}
		if err := s.nodeEndpointsSnapshot(nodeID); err != nil {
			log.Logger.Error("Failed to update endpoints snapshot for node", "node", nodeID, "error", err)
		}
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
	s.deleteNode(node.GetId())
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
	// Verify peer is not exceeding requests rate limit
	if err := waitForRequestLimit(ctx, stream.requestRateLimit); err != nil {
		log.Logger.Warn("Peer exceeded rate limit", "error", err, "peer", stream.peerAddress)
		return status.Errorf(codes.ResourceExhausted, "stream request rate limit exceeded: %v", err)
	}
	// Verify server's global request limit
	if err := waitForRequestLimit(ctx, s.requestRateLimit); err != nil {
		log.Logger.Warn("Sever total requests rate limit exceeded", "error", err, "peer", stream.peerAddress)
		return status.Errorf(codes.ResourceExhausted, "stream request rate limit exceeded: %v", err)
	}
	// Legacy empty nodeID client
	if r.GetNode().GetId() == EmptyNodeID {
		log.Logger.Warn("Client using empty string as node id", "client", stream.peerAddress)
		return nil
	}

	s.addNewNode(r.GetNode().GetId())
	if s.needToUpdateSnapshot(r.GetNode().GetId(), r.GetTypeUrl(), r.GetResourceNames()) {
		if err := s.updateNodeSnapshot(r.GetNode().GetId(), r.GetTypeUrl(), r.GetResourceNames()); err != nil {
			return err
		}
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

// addNewNode adds a new node with empty resources to the nodes map. It uses the
// nodeID to determine whether a new addition is needed. Will do nothing for existing nodes
func (s *Snapshotter) addNewNode(nodeID string) {
	s.nodesLock.Lock()
	defer s.nodesLock.Unlock()
	if _, ok := s.nodes[nodeID]; !ok {
		// Node not found, add a new one without any resources
		log.Logger.Info("Node cache not found, initialising", "node", nodeID)
		serviceResources := map[string][]types.Resource{
			resource.ClusterType:  []types.Resource{},
			resource.ListenerType: []types.Resource{},
			resource.RouteType:    []types.Resource{},
		}
		serviceResourcesNames := map[string][]string{
			resource.ClusterType:  []string{},
			resource.ListenerType: []string{},
			resource.RouteType:    []string{},
		}
		enspointsResources := map[string][]types.Resource{
			resource.EndpointType: []types.Resource{},
		}
		endpointsResourcesNames := map[string][]string{
			resource.EndpointType: []string{},
		}
		s.nodes[nodeID] = &NodeSnapshotResources{
			serviceResources:        serviceResources,
			serviceResourcesNames:   serviceResourcesNames,
			endpointsResources:      enspointsResources,
			endpointsResourcesNames: endpointsResourcesNames,
		}
	}
}

// deleteNode removes a node from nodes map and clears existing snaphots for the node
func (s *Snapshotter) deleteNode(nodeID string) {
	if nodeID == EmptyNodeID {
		return
	}
	s.nodesLock.Lock()
	defer s.nodesLock.Unlock()
	delete(s.nodes, nodeID)
	s.servicesCache.ClearSnapshot(nodeID)
	s.endpointsCache.ClearSnapshot(nodeID)
}

// getResourcesFromCache returns a set of resources from the full resources snapshot (all watched cluster resources)
func (s *Snapshotter) getResourcesFromCache(typeURL string, resources []string) ([]types.Resource, error) {
	var fullResourcesSnap cache.ResourceSnapshot
	var err error
	if mapTypeURL(typeURL) == "services" {
		fullResourcesSnap, err = s.servicesCache.GetSnapshot(EmptyNodeID)
		if err != nil {
			return []types.Resource{}, fmt.Errorf("Cannot get full resources snaphot from cache")
		}
	}
	if mapTypeURL(typeURL) == "endpoints" {
		fullResourcesSnap, err = s.endpointsCache.GetSnapshot(EmptyNodeID)
		if err != nil {
			return []types.Resource{}, fmt.Errorf("Cannot get full resources snaphot from cache")
		}
	}
	fullResources := fullResourcesSnap.GetResources(typeURL)
	res := []types.Resource{}
	for _, name := range resources {
		r, ok := fullResources[name]
		if !ok {
			return res, fmt.Errorf("Requested resource: %s of type: %s not found", name, typeURL)
		}
		res = append(res, r)
	}
	return res, nil
}

// updateNodeServiceSnapshotResources goes through the full snapshot and copies resources in the respective
// node resources struct
func (s *Snapshotter) updateNodeServiceSnapshotResources(nodeID, typeURL string, resources []string) error {
	s.nodesLock.Lock()
	defer s.nodesLock.Unlock()
	node, ok := s.nodes[nodeID]
	if !ok {
		return fmt.Errorf("Cannot update service snapshot resources, node: %s not found", nodeID)
	}
	newSnapResources, err := s.getResourcesFromCache(typeURL, resources)
	if err != nil {
		return fmt.Errorf("Cannot get resources from cache: %s", err)
	}
	node.serviceResources[typeURL] = newSnapResources
	node.serviceResourcesNames[typeURL] = resources
	s.nodes[nodeID] = node
	return nil
}

// updateNodeEndpointsSnapshotResources goes through the full snapshot and copies resources in the respective
// node resources struct
func (s *Snapshotter) updateNodeEndpointsSnapshotResources(nodeID, typeURL string, resources []string) error {
	s.nodesLock.Lock()
	defer s.nodesLock.Unlock()
	node, ok := s.nodes[nodeID]
	if !ok {
		return fmt.Errorf("Cannot update service snapshot resources, node: %s not found", nodeID)
	}
	newSnapResources, err := s.getResourcesFromCache(typeURL, resources)
	if err != nil {
		return fmt.Errorf("Cannot get resources from cache: %s", err)
	}
	node.endpointsResources[typeURL] = newSnapResources
	node.endpointsResourcesNames[typeURL] = resources
	s.nodes[nodeID] = node
	return nil
}

// nodeServiceSnapshot throws the current service NodeResources content into a new snapshot
func (s *Snapshotter) nodeServiceSnapshot(nodeID string) error {
	ctx := context.Background()
	s.nodesLock.RLock()
	node, ok := s.nodes[nodeID]
	if !ok {
		return fmt.Errorf("Cannot create a new snapshot, node: %s not found", nodeID)
	}
	s.nodesLock.RUnlock()
	atomic.AddInt32(&node.serviceSnapVersion, 1)
	snapshot, err := cache.NewSnapshot(fmt.Sprint(node.serviceSnapVersion), node.serviceResources)
	if err != nil {
		return err
	}
	return s.servicesCache.SetSnapshot(ctx, nodeID, snapshot)
}

// nodeEndpointsSnapshot throws the current endpoints NodeResources content into a new snapshot
func (s *Snapshotter) nodeEndpointsSnapshot(nodeID string) error {
	ctx := context.Background()
	s.nodesLock.RLock()
	node, ok := s.nodes[nodeID]
	if !ok {
		return fmt.Errorf("Cannot create a new snapshot, node: %s not found", nodeID)
	}
	s.nodesLock.RUnlock()
	atomic.AddInt32(&node.endpointsSnapVersion, 1)
	snapshot, err := cache.NewSnapshot(fmt.Sprint(node.endpointsSnapVersion), node.endpointsResources)
	if err != nil {
		return err
	}
	return s.endpointsCache.SetSnapshot(ctx, nodeID, snapshot)
}

// updateNodeSnapshot will update the node snapshot for the requested resources type based on
// data found in the full resources snapshot
func (s *Snapshotter) updateNodeSnapshot(nodeID, typeURL string, resources []string) error {
	if mapTypeURL(typeURL) == "services" {
		if err := s.updateNodeServiceSnapshotResources(nodeID, typeURL, resources); err != nil {
			return err
		}
		return s.nodeServiceSnapshot(nodeID)
	}
	if mapTypeURL(typeURL) == "endpoints" {
		if err := s.updateNodeEndpointsSnapshotResources(nodeID, typeURL, resources); err != nil {
			return err
		}
		return s.nodeEndpointsSnapshot(nodeID)
	}
	return nil
}

// needToUpdateSnapshot checks id a node snapshot needs updating based on the requested resources
// from the client
func (s *Snapshotter) needToUpdateSnapshot(nodeID, typeURL string, resources []string) bool {
	s.nodesLock.RLock()
	node, ok := s.nodes[nodeID]
	if !ok {
		return false
	}
	s.nodesLock.RUnlock()
	if mapTypeURL(typeURL) == "services" {
		return !resourcesMatch(node.serviceResourcesNames[typeURL], resources)
	}
	if mapTypeURL(typeURL) == "endpoints" {
		return !resourcesMatch(node.endpointsResourcesNames[typeURL], resources)
	}
	return false
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
