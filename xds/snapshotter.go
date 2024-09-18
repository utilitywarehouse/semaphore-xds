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
	authority              string // Authority name of the server for federated requests
	servePort              uint
	servicesCache          cache.SnapshotCache // default service snapshot cache for empty node ID (all watched resources snapshot)
	serviceSnapVersion     int32               // Service snap version for empty node ID snapshot
	endpointsCache         cache.SnapshotCache // default endpoints snapshot cache for empty node ID (all watched resources snapshot)
	endpointsSnapVersion   int32               // Endpoints snap version for empty node ID snapshot
	muxCache               cache.MuxCache
	requestRateLimit       *rate.Limiter // maximum number of requests allowed to server
	streamRequestPerSecond float64       // maximum number of requests per stream per second
	streams                sync.Map      // map of open streams
	nodes                  sync.Map      // maps all clients node ids to requested resources that will be snapshotted and served
	snapNodesMu            sync.Mutex    // Simple lock to avoid deleting a node while snapshotting
}

// Node keeps the info for a node. Each node can have multiple open streams,
// each one requesting resources that should be part of the cache snapshot for
// the specific node.
type Node struct {
	address              string
	resources            map[int64]*NodeSnapshotResources // map of node resources per open stream id
	serviceSnapVersion   int32                            // Service snap version for node specific cache snapshot
	endpointsSnapVersion int32                            // Endpoints snap version for node specific cache snapshot
}

// NodeSnapshot keeps resources and versions to help snapshotting per node
type NodeSnapshotResources struct {
	services       map[string][]types.Resource
	servicesNames  map[string][]string
	endpoints      map[string][]types.Resource
	endpointsNames map[string][]string
}

// Deep copy function for Node resources
func deepCopyNodeResources(src map[int64]*NodeSnapshotResources) map[int64]*NodeSnapshotResources {
	dst := make(map[int64]*NodeSnapshotResources, len(src))

	copyMap := func(src, dst map[string][]types.Resource) {
		for k, v := range src {
			dst[k] = make([]types.Resource, len(v))
			copy(dst[k], v)
		}
	}
	copyStringMap := func(src, dst map[string][]string) {
		for k, v := range src {
			dst[k] = make([]string, len(v))
			copy(dst[k], v)
		}
	}

	for sID, resources := range src {
		r := &NodeSnapshotResources{
			services:       make(map[string][]types.Resource, len(resources.services)),
			servicesNames:  make(map[string][]string, len(resources.servicesNames)),
			endpoints:      make(map[string][]types.Resource, len(resources.endpoints)),
			endpointsNames: make(map[string][]string, len(resources.endpointsNames)),
		}

		copyMap(resources.services, r.services)
		copyStringMap(resources.servicesNames, r.servicesNames)
		copyMap(resources.endpoints, r.endpoints)
		copyStringMap(resources.endpointsNames, r.endpointsNames)

		dst[sID] = r
	}
	return dst
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
func NewSnapshotter(authority string, port uint, requestLimit, streamRequestLimit float64) *Snapshotter {
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
		authority:              authority,
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
		node := n.(*Node)
		r[nodeID] = node.address
		return true
	})
	return r
}

// SnapServices dumps the list of watched Kubernetes Services into services
// snapshots.
func (s *Snapshotter) SnapServices(serviceStore XdsServiceStore) error {
	ctx := context.Background()
	cls, rds, lsnr, err := servicesToResources(serviceStore, "")
	if err != nil {
		return fmt.Errorf("Failed to snapshot Services: %v", err)
	}
	// if authority is set, also snapshot based on xdstp names
	if s.authority != "" {
		xdstpCLS, xdstpRDS, xdstpLSNR, err := servicesToResources(serviceStore, s.authority)
		if err != nil {
			return fmt.Errorf("Failed to snapshot Services: %v", err)
		}
		cls = append(cls, xdstpCLS...)
		rds = append(rds, xdstpRDS...)
		lsnr = append(lsnr, xdstpLSNR...)
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
	s.snapNodesMu.Lock()
	defer s.snapNodesMu.Unlock()
	s.nodes.Range(func(nID, n interface{}) bool {
		nodeID := nID.(string)
		node := n.(*Node)
		for sID, res := range node.resources {
			for typeURL, resources := range res.servicesNames {
				if err := s.updateNodeStreamResources(nodeID, typeURL, sID, resources); err != nil {
					log.Logger.Error("Failed to update service resources before snapping",
						"type", typeURL, "node", nodeID,
						"resources", resources, "stream_id", sID,
						"error", err,
					)
				}
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
// endoints snapshots.
func (s *Snapshotter) SnapEndpoints(endpointStore XdsEndpointStore) error {
	ctx := context.Background()
	eds, err := endpointSlicesToClusterLoadAssignments(endpointStore, "")
	if err != nil {
		return fmt.Errorf("Failed to snapshot EndpointSlices: %v", err)
	}
	// if authority is set, also snapshot based on xdstp names
	if s.authority != "" {
		xdstpEDS, err := endpointSlicesToClusterLoadAssignments(endpointStore, s.authority)
		if err != nil {
			return fmt.Errorf("Failed to snapshot EndpointSlices: %v", err)
		}
		eds = append(eds, xdstpEDS...)
	}
	atomic.AddInt32(&s.endpointsSnapVersion, 1)
	resources := map[string][]types.Resource{
		resource.EndpointType: eds,
	}
	snapshot, err := cache.NewSnapshot(fmt.Sprint(s.endpointsSnapVersion), resources)
	if err != nil {
		return fmt.Errorf("Failed to create snapshot: %v", err)
	}
	err = s.endpointsCache.SetSnapshot(ctx, EmptyNodeID, snapshot)
	if err != nil {
		return fmt.Errorf("Failed to set endpoints snapshot %v", err)
	}
	s.snapNodesMu.Lock()
	defer s.snapNodesMu.Unlock()
	s.nodes.Range(func(nID, n interface{}) bool {
		nodeID := nID.(string)
		node := n.(*Node)
		for sID, res := range node.resources {
			for typeURL, resources := range res.endpointsNames {
				if err := s.updateNodeStreamEndpointsResources(nodeID, typeURL, sID, resources); err != nil {
					log.Logger.Error("Failed to update endpoints resources before snapping",
						"type", typeURL, "node", nodeID,
						"resources", resources, "stream_id", sID,
						"error", err,
					)
				}
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
	s.streams.Store(id, &Stream{
		peerAddress:      peerAddr,
		requestRateLimit: rate.NewLimiter(rate.Limit(s.streamRequestPerSecond), 1),
	})
	metricOnStreamOpenInc()
	return nil
}

func (s *Snapshotter) OnStreamClosed(id int64, node *core.Node) {
	log.Logger.Info("OnStreamClosed", "id", id, "node", node)
	s.streams.Delete(id)
	s.deleteNodeStream(node.GetId(), id)
	metricOnStreamClosedInc()
}

func (s *Snapshotter) OnStreamRequest(id int64, r *discovery.DiscoveryRequest) error {
	ctx := context.Background()
	st, _ := s.streams.Load(id)
	stream := st.(*Stream)
	log.Logger.Info("OnStreamRequest",
		"id", id,
		"peer", stream.peerAddress,
		"received", r.GetTypeUrl(),
		"node", r.GetNode().GetId(),
		"locality", r.GetNode().GetLocality(),
		"names", strings.Join(r.GetResourceNames(), ", "),
		"version", r.GetVersionInfo(),
	)
	metricOnStreamRequestInc(r.GetTypeUrl())
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

	s.addOrUpdateNode(r.GetNode().GetId(), stream.peerAddress, id)
	if s.needToUpdateSnapshot(r.GetNode().GetId(), r.GetTypeUrl(), id, r.GetResourceNames()) {
		if err := s.updateStreamNodeResources(r.GetNode().GetId(), r.GetTypeUrl(), id, r.GetResourceNames()); err != nil {
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
	metricOnStreamResponseInc(resp.GetTypeUrl())
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

// addOrUpdateNode will add a new node if not present, or add a new stream
// resources placeholder if needed.
func (s *Snapshotter) addOrUpdateNode(nodeID, address string, streamID int64) {
	n, ok := s.nodes.Load(nodeID)
	if !ok {
		// Node not found, add a new one without any resources
		log.Logger.Info("Node cache not found, initialising", "node", nodeID)
		s.nodes.Store(nodeID, &Node{
			address: address,
			resources: map[int64]*NodeSnapshotResources{
				streamID: makeEmptyNodeResources(),
			},
		})
		return
	}
	node := n.(*Node)
	nodeResources := deepCopyNodeResources(node.resources)
	for sID, _ := range nodeResources {
		if sID == streamID {
			return // Stream already know for node
		}
	}
	log.Logger.Info("New stream for node", "node", nodeID, "streamID", streamID)
	nodeResources[streamID] = makeEmptyNodeResources()
	updatedNode := &Node{
		address:   node.address,
		resources: nodeResources,
	}
	s.nodes.Store(nodeID, updatedNode)
}

// deleteNodeStream removes a stream from a node's resources and if the list of streams is
// empty deletes the node
func (s *Snapshotter) deleteNodeStream(nodeID string, streamID int64) {
	if nodeID == EmptyNodeID {
		return
	}
	n, ok := s.nodes.Load(nodeID)
	if !ok {
		log.Logger.Warn("Tried to delete stream for non existing node", "node", nodeID, "stream_id", streamID)
		return
	}
	node := n.(*Node)
	nodeResources := deepCopyNodeResources(node.resources)
	delete(nodeResources, streamID)
	// if no more streams are open, delete the node
	if len(nodeResources) == 0 {
		s.deleteNode(nodeID)
		return
	}
	// else just update the node
	updatedNode := &Node{
		address:   node.address,
		resources: nodeResources,
	}
	s.nodes.Store(nodeID, updatedNode)
	if err := s.nodeServiceSnapshot(nodeID); err != nil {
		log.Logger.Warn("Failed to update service snapshot on stream closure", "node", nodeID, "error", err)
	}
	if err := s.nodeEndpointsSnapshot(nodeID); err != nil {
		log.Logger.Warn("Failed to update endpoints snapshot on stream closure", "node", nodeID, "error", err)
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

// updateNodeStreamResources updates the list of service resources requested in a node's stream context
// by copying the most up to date version of them from the full snapshot (default snapshot)
func (s *Snapshotter) updateNodeStreamResources(nodeID, typeURL string, streamID int64, resources []string) error {
	n, ok := s.nodes.Load(nodeID)
	if !ok {
		return fmt.Errorf("Cannot update service snapshot resources, node: %s not found", nodeID)
	}
	node := n.(*Node)
	nodeResources := deepCopyNodeResources(node.resources)
	if _, ok := nodeResources[streamID]; !ok {
		return fmt.Errorf("Cannot find service resources to update for node: %s in stream: %d context", nodeID, streamID)
	}

	newSnapResources, err := s.getResourcesFromCache(typeURL, resources)
	if err != nil {
		return fmt.Errorf("Cannot get resources from cache: %s", err)
	}

	nodeResources[streamID].services[typeURL] = newSnapResources
	nodeResources[streamID].servicesNames[typeURL] = resources
	updatedNode := &Node{
		address:   node.address,
		resources: nodeResources,
	}
	s.nodes.Store(nodeID, updatedNode)
	return nil
}

// updateNodeStreamEndpointsResources updates the list of endpoint resources requested in a node's stream context
// by copying the most up to date version of them from the full snapshot (default snapshot)

func (s *Snapshotter) updateNodeStreamEndpointsResources(nodeID, typeURL string, streamID int64, resources []string) error {
	n, ok := s.nodes.Load(nodeID)
	if !ok {
		return fmt.Errorf("Cannot update endpoint snapshot resources, node: %s not found", nodeID)
	}
	node := n.(*Node)
	nodeResources := deepCopyNodeResources(node.resources)
	if _, ok := nodeResources[streamID]; !ok {
		return fmt.Errorf("Cannot find endpoint resources to update for node: %s in stream: %d context", nodeID, streamID)
	}

	newSnapResources, err := s.getResourcesFromCache(typeURL, resources)
	if err != nil {
		return fmt.Errorf("Cannot get resources from cache: %s", err)
	}

	nodeResources[streamID].endpoints[typeURL] = newSnapResources
	nodeResources[streamID].endpointsNames[typeURL] = resources
	updatedNode := &Node{
		address:   node.address,
		resources: nodeResources,
	}
	s.nodes.Store(nodeID, updatedNode)
	return nil
}

// nodeServiceSnapshot throws the current service NodeResources content into a new snapshot
func (s *Snapshotter) nodeServiceSnapshot(nodeID string) error {
	ctx := context.Background()
	n, ok := s.nodes.Load(nodeID)
	if !ok {
		return fmt.Errorf("Cannot create a new snapshot, node: %s not found", nodeID)
	}
	node := n.(*Node)
	atomic.AddInt32(&node.serviceSnapVersion, 1)
	snapServices := aggregateNodeResources("services", node.resources)
	snapshot, err := cache.NewSnapshot(fmt.Sprint(node.serviceSnapVersion), snapServices)
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
	node := n.(*Node)
	atomic.AddInt32(&node.endpointsSnapVersion, 1)
	snapEndpoints := aggregateNodeResources("endpoints", node.resources)
	snapshot, err := cache.NewSnapshot(fmt.Sprint(node.endpointsSnapVersion), snapEndpoints)
	if err != nil {
		return err
	}
	return s.endpointsCache.SetSnapshot(ctx, nodeID, snapshot)
}

// updateStreamNodeResources will update the resources tracked for the node inside a streams context and
// trigger a new snapshot
func (s *Snapshotter) updateStreamNodeResources(nodeID, typeURL string, streamID int64, resources []string) error {
	if mapTypeURL(typeURL) == "services" {
		if err := s.updateNodeStreamResources(nodeID, typeURL, streamID, resources); err != nil {
			return err
		}
		return s.nodeServiceSnapshot(nodeID)
	}
	if mapTypeURL(typeURL) == "endpoints" {
		if err := s.updateNodeStreamEndpointsResources(nodeID, typeURL, streamID, resources); err != nil {
			return err
		}
		return s.nodeEndpointsSnapshot(nodeID)
	}
	return nil
}

// needToUpdateSnapshot checks id a node snapshot needs updating based on the requested resources
// from the client inside a streams context
func (s *Snapshotter) needToUpdateSnapshot(nodeID, typeURL string, streamID int64, resources []string) bool {
	n, ok := s.nodes.Load(nodeID)
	if !ok {
		return false
	}
	node := n.(*Node)
	sNodeResources, ok := node.resources[streamID]
	if !ok {
		log.Logger.Warn("Cannot check if snapshot needs updating, strema not found", "id", streamID)
		return false
	}
	if mapTypeURL(typeURL) == "services" {
		return !resourcesMatch(sNodeResources.servicesNames[typeURL], resources)
	}
	if mapTypeURL(typeURL) == "endpoints" {
		return !resourcesMatch(sNodeResources.endpointsNames[typeURL], resources)
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

// aggregateNodeResources places all resources requested under different node streams in
// the same list for creating a new snapshot
func aggregateNodeResources(resourceType string, nodeResources map[int64]*NodeSnapshotResources) map[string][]types.Resource {
	aggrResources := map[string][]types.Resource{}
	if resourceType == "services" {
		for _, r := range nodeResources {
			for typeUrl, resources := range r.services {
				for _, resource := range resources {
					aggrResources[typeUrl] = append(aggrResources[typeUrl], resource)
				}
			}
		}
	}
	if resourceType == "endpoints" {
		for _, r := range nodeResources {
			for typeUrl, resources := range r.endpoints {
				for _, resource := range resources {
					aggrResources[typeUrl] = append(aggrResources[typeUrl], resource)
				}
			}
		}
	}
	return aggrResources
}

func makeEmptyNodeResources() *NodeSnapshotResources {
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
	return &NodeSnapshotResources{
		services:       services,
		servicesNames:  servicesNames,
		endpoints:      enspointsResources,
		endpointsNames: endpointsNames,
	}
}
