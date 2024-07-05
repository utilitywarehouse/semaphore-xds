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
	requestRateLimit       *rate.Limiter // maximum number of requests allowed to server
	streamRequestPerSecond float64       // maximum number of requests per stream per second
	streams                sync.Map      // map of open streams
	nodes                  sync.Map      // maps all clients node ids to requested resources that will be snapshotted and served
	snapNodesMu            sync.Mutex    // Simple lock to avoid deleting a node while snapshotting
}

// Node keeps the info for a node
type Node struct {
	address   string
	resources *NodeSnapshotResources
}

// NodeSnapshot keeps resources and versions to help snapshotting per node
type NodeSnapshotResources struct {
	services             map[string][]types.Resource
	servicesNames        map[string][]string
	serviceSnapVersion   int32
	endpoints            map[string][]types.Resource
	endpointsNames       map[string][]string
	endpointsSnapVersion int32
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
	}
}

func (s *Snapshotter) ServicesSnapshot(nodeID string) (cache.ResourceSnapshot, error) {
	return s.servicesCache.GetSnapshot(nodeID)
}

func (s *Snapshotter) EndpointsSnapshot(nodeID string) (cache.ResourceSnapshot, error) {
	return s.endpointsCache.GetSnapshot(nodeID)
}

// NodesMap returns a map of node ids to addresses
func (s *Snapshotter) NodesMap() map[string]string {
	r := make(map[string]string)
	s.nodes.Range(func(nID, n interface{}) bool {
		nodeID := nID.(string)
		node := n.(Node)
		r[nodeID] = node.address
		return true
	})
	return r
}

// SnapServices dumps the list of watched Kubernetes Services into services
// snapshot
func (s *Snapshotter) SnapServices(serviceStore XdsServiceStore) error {
	ctx := context.Background()
	s.snapNodesMu.Lock()
	defer s.snapNodesMu.Unlock()
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
	s.nodes.Range(func(nID, n interface{}) bool {
		nodeID := nID.(string)
		node := n.(Node)
		for typeURL, resources := range node.resources.servicesNames {
			if err := s.updateNodeServiceSnapshotResources(nodeID, typeURL, resources); err != nil {
				log.Logger.Error("Failed to update service resources before snapping", "type", typeURL, "node", nodeID, "resources", resources, "error", err)
			}
		}
		if err := s.nodeServiceSnapshot(nodeID); err != nil {
			log.Logger.Error("Failed to update service snapshot for node", "node", nodeID, "error", err)
		}
		return true
	})
	return nil
}

// SnapEndpoints dumps the list of watched Kubernetes EndpointSlices into
// endoints snapshot
func (s *Snapshotter) SnapEndpoints(endpointStore XdsEndpointStore) error {
	ctx := context.Background()
	s.snapNodesMu.Lock()
	defer s.snapNodesMu.Unlock()
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
	s.nodes.Range(func(nID, n interface{}) bool {
		nodeID := nID.(string)
		node := n.(Node)
		for typeURL, resources := range node.resources.endpointsNames {
			if err := s.updateNodeEndpointsSnapshotResources(nodeID, typeURL, resources); err != nil {
				log.Logger.Error("Failed to update endpoints resources before snapping", "type", typeURL, "node", nodeID, "resources", resources, "error", err)
			}
		}
		if err := s.nodeEndpointsSnapshot(nodeID); err != nil {
			log.Logger.Error("Failed to update endpoints snapshot for node", "node", nodeID, "error", err)
		}
		return true
	})
	return nil
}

func (s *Snapshotter) OnStreamOpen(ctx context.Context, id int64, typ string) error {
	var peerAddr string
	if peerInfo, ok := peer.FromContext(ctx); ok {
		peerAddr = peerInfo.Addr.String()
	}
	log.Logger.Info("OnStreamOpen", "peer address", peerAddr, "id", id, "type", typ)
	s.streams.Store(id, Stream{
		peerAddress:      peerAddr,
		requestRateLimit: rate.NewLimiter(rate.Limit(s.streamRequestPerSecond), 1),
	})
	metricOnStreamOpenInc(peerAddr)
	return nil
}

func (s *Snapshotter) OnStreamClosed(id int64, node *core.Node) {
	log.Logger.Info("OnStreamClosed", "id", id, "node", node)
	st, _ := s.streams.Load(id)
	stream := st.(Stream)
	s.streams.Delete(id)
	go s.deleteNode(node.GetId())
	metricOnStreamClosedInc(stream.peerAddress)
}

func (s *Snapshotter) OnStreamRequest(id int64, r *discovery.DiscoveryRequest) error {
	ctx := context.Background()
	st, _ := s.streams.Load(id)
	stream := st.(Stream)
	log.Logger.Info("OnStreamRequest",
		"id", id,
		"peer", stream.peerAddress,
		"received", r.GetTypeUrl(),
		"node", r.GetNode().GetId(),
		"locality", r.GetNode().GetLocality(),
		"names", strings.Join(r.GetResourceNames(), ", "),
		"version", r.GetVersionInfo(),
	)
	metricOnStreamRequestInc(r.GetNode().GetId(), stream.peerAddress, r.GetTypeUrl())
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
		log.Logger.Info("Client using empty string as node id", "client", stream.peerAddress)
		return nil
	}

	s.addNewNode(r.GetNode().GetId(), stream.peerAddress)
	if s.needToUpdateSnapshot(r.GetNode().GetId(), r.GetTypeUrl(), r.GetResourceNames()) {
		if err := s.updateNodeSnapshot(r.GetNode().GetId(), r.GetTypeUrl(), r.GetResourceNames()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Snapshotter) OnStreamResponse(ctx context.Context, id int64, req *discovery.DiscoveryRequest, resp *discovery.DiscoveryResponse) {
	st, _ := s.streams.Load(id)
	stream := st.(Stream)
	log.Logger.Info("OnStreamResponse",
		"id", id,
		"type", resp.GetTypeUrl(),
		"version", resp.GetVersionInfo(),
		"resources", len(resp.GetResources()),
	)
	metricOnStreamResponseInc(req.GetNode().GetId(), stream.peerAddress, resp.GetTypeUrl())
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
func (s *Snapshotter) addNewNode(nodeID, address string) {
	if _, ok := s.nodes.Load(nodeID); !ok {
		// Node not found, add a new one without any resources
		log.Logger.Info("Node cache not found, initialising", "node", nodeID)
		services := map[string][]types.Resource{
			resource.ClusterType:  []types.Resource{},
			resource.ListenerType: []types.Resource{},
			resource.RouteType:    []types.Resource{},
		}
		servicesNames := map[string][]string{
			resource.ClusterType:  []string{},
			resource.ListenerType: []string{},
			resource.RouteType:    []string{},
		}
		enspointsResources := map[string][]types.Resource{
			resource.EndpointType: []types.Resource{},
		}
		endpointsNames := map[string][]string{
			resource.EndpointType: []string{},
		}
		s.nodes.Store(nodeID, Node{
			address: address,
			resources: &NodeSnapshotResources{
				services:       services,
				servicesNames:  servicesNames,
				endpoints:      enspointsResources,
				endpointsNames: endpointsNames,
			},
		})
	}
}

// deleteNode removes a node from nodes map and clears existing snaphots for the node
func (s *Snapshotter) deleteNode(nodeID string) {
	if nodeID == EmptyNodeID {
		return
	}
	s.snapNodesMu.Lock()
	defer s.snapNodesMu.Unlock()
	s.nodes.Delete(nodeID)
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
			// If a resource is not found, do not fail, which will result in closing the stream.
			// Instead log an warning and keep populating the resources list
			log.Logger.Warn(
				"Requested resource not found",
				"name", name,
				"type", typeURL,
			)
			continue
		}
		res = append(res, r)
	}
	return res, nil
}

// updateNodeServiceSnapshotResources goes through the full snapshot and copies resources in the respective
// node resources struct
func (s *Snapshotter) updateNodeServiceSnapshotResources(nodeID, typeURL string, resources []string) error {
	n, ok := s.nodes.Load(nodeID)
	if !ok {
		return fmt.Errorf("Cannot update service snapshot resources, node: %s not found", nodeID)
	}
	node := n.(Node)
	newSnapResources, err := s.getResourcesFromCache(typeURL, resources)
	if err != nil {
		return fmt.Errorf("Cannot get resources from cache: %s", err)
	}
	node.resources.services[typeURL] = newSnapResources
	node.resources.servicesNames[typeURL] = resources
	s.nodes.Store(nodeID, node)
	return nil
}

// updateNodeEndpointsSnapshotResources goes through the full snapshot and copies resources in the respective
// node resources struct
func (s *Snapshotter) updateNodeEndpointsSnapshotResources(nodeID, typeURL string, resources []string) error {
	n, ok := s.nodes.Load(nodeID)
	if !ok {
		return fmt.Errorf("Cannot update service snapshot resources, node: %s not found", nodeID)
	}
	node := n.(Node)
	newSnapResources, err := s.getResourcesFromCache(typeURL, resources)
	if err != nil {
		return fmt.Errorf("Cannot get resources from cache: %s", err)
	}
	node.resources.endpoints[typeURL] = newSnapResources
	node.resources.endpointsNames[typeURL] = resources
	s.nodes.Store(nodeID, node)
	return nil
}

// nodeServiceSnapshot throws the current service NodeResources content into a new snapshot
func (s *Snapshotter) nodeServiceSnapshot(nodeID string) error {
	ctx := context.Background()
	n, ok := s.nodes.Load(nodeID)
	if !ok {
		return fmt.Errorf("Cannot create a new snapshot, node: %s not found", nodeID)
	}
	node := n.(Node)
	atomic.AddInt32(&node.resources.serviceSnapVersion, 1)
	snapshot, err := cache.NewSnapshot(fmt.Sprint(node.resources.serviceSnapVersion), node.resources.services)
	if err != nil {
		return err
	}
	return s.servicesCache.SetSnapshot(ctx, nodeID, snapshot)
}

// nodeEndpointsSnapshot throws the current endpoints NodeResources content into a new snapshot
func (s *Snapshotter) nodeEndpointsSnapshot(nodeID string) error {
	ctx := context.Background()
	n, ok := s.nodes.Load(nodeID)
	if !ok {
		return fmt.Errorf("Cannot create a new snapshot, node: %s not found", nodeID)
	}
	node := n.(Node)
	atomic.AddInt32(&node.resources.endpointsSnapVersion, 1)
	snapshot, err := cache.NewSnapshot(fmt.Sprint(node.resources.endpointsSnapVersion), node.resources.endpoints)
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
	n, ok := s.nodes.Load(nodeID)
	if !ok {
		return false
	}
	node := n.(Node)
	if mapTypeURL(typeURL) == "services" {
		return !resourcesMatch(node.resources.servicesNames[typeURL], resources)
	}
	if mapTypeURL(typeURL) == "endpoints" {
		return !resourcesMatch(node.resources.endpointsNames[typeURL], resources)
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
