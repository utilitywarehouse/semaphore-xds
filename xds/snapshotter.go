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
	/// copying the EmptyNodeID. Also, we can use it when exporting metrics to
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
	servicesCache          cache.SnapshotCache
	serviceSnapVersion     int32 // Service snap version for empty node ID snapshot
	endpointsCache         cache.SnapshotCache
	endpointsSnapVersion   int32 // Endpoints snap version for empty node ID snapshot
	deltaCDSCache          *cache.LinearCache
	deltaEDSCache          *cache.LinearCache
	deltaRDSCache          *cache.LinearCache
	deltaVHDSCache         *cache.LinearCache
	muxCache               cache.MuxCache
	requestRateLimit       *rate.Limiter    // maximum number of requests allowed to server
	streamRequestPerSecond float64          // maximum number of requests per stream per second
	streams                sync.Map         // map of open streams
	nodes                  map[string]*Node // maps all clients node ids to requested resources that will be snapshotted and served
	nodesMu                sync.RWMutex
	snapNodesMu            sync.Mutex // Simple lock to avoid deleting a node while snapshotting
	localhostEndpoints     bool
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

func mapDeltaTypeURL(typeURL string) string {
	switch typeURL {
	case resource.ClusterType:
		return "deltaClusters"
	case resource.EndpointType:
		return "deltaEndpoints"
	case resource.RouteType:
		return "deltaRouteConfigurations"
	case resource.VirtualHostType:
		return "deltaVirtualHosts"
	default:
		return ""
	}
}

func (s *Snapshotter) getCacheForType(typeURL string) cache.SnapshotCache {
	switch typeURL {
	case resource.ListenerType, resource.RouteType, resource.ClusterType:
		return s.servicesCache
	case resource.EndpointType:
		return s.endpointsCache
	default:
		return nil
	}
}

func (s *Snapshotter) getDeltaCacheForType(typeURL string) *cache.LinearCache {
	switch typeURL {
	case resource.EndpointType:
		return s.deltaEDSCache
	case resource.ClusterType:
		return s.deltaCDSCache
	case resource.RouteType:
		return s.deltaRDSCache
	case resource.VirtualHostType:
		return s.deltaVHDSCache
	default:
		return nil
	}
}

// NewSnapshotter needs a grpc server port and the allowed requests limits per server and stream per second
func NewSnapshotter(authority string, port uint, requestLimit, streamRequestLimit float64, localhostEndpoints bool) *Snapshotter {
	servicesCache := cache.NewSnapshotCache(false, cache.IDHash{}, log.EnvoyLogger)
	endpointsCache := cache.NewSnapshotCache(false, cache.IDHash{}, log.EnvoyLogger)
	deltaCDSCache := cache.NewLinearCache(resource.ClusterType, cache.WithLogger(log.EnvoyLogger))
	deltaEDSCache := cache.NewLinearCache(resource.EndpointType, cache.WithLogger(log.EnvoyLogger))
	deltaRDSCache := cache.NewLinearCache(resource.RouteType, cache.WithLogger(log.EnvoyLogger))
	deltaVHDSCache := cache.NewLinearCache(resource.VirtualHostType, cache.WithLogger(log.EnvoyLogger))
	muxCache := cache.MuxCache{
		Classify: func(r *cache.Request) string {
			return mapTypeURL(r.TypeUrl)
		},
		ClassifyDelta: func(r *cache.DeltaRequest) string {
			return mapDeltaTypeURL(r.TypeUrl)
		},
		Caches: map[string]cache.Cache{
			"services":                 servicesCache,
			"endpoints":                endpointsCache,
			"deltaClusters":            deltaCDSCache,
			"deltaEndpoints":           deltaEDSCache,
			"deltaRouteConfigurations": deltaRDSCache,
			"deltaVirtualHosts":        deltaVHDSCache,
		},
	}
	return &Snapshotter{
		authority:              authority,
		servePort:              port,
		servicesCache:          servicesCache,
		endpointsCache:         endpointsCache,
		deltaCDSCache:          deltaCDSCache,
		deltaEDSCache:          deltaEDSCache,
		deltaRDSCache:          deltaRDSCache,
		deltaVHDSCache:         deltaVHDSCache,
		muxCache:               muxCache,
		nodes:                  make(map[string]*Node),
		requestRateLimit:       rate.NewLimiter(rate.Limit(requestLimit), 1),
		streamRequestPerSecond: streamRequestLimit,
		localhostEndpoints:     localhostEndpoints,
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
	s.nodesMu.RLock()
	defer s.nodesMu.RUnlock()
	for nodeID, node := range s.nodes {
		r[nodeID] = node.address
	}
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
	s.snapNodesMu.Lock()
	defer s.snapNodesMu.Unlock()
	s.serviceSnapVersion += int32(1)
	resources := map[string][]types.Resource{
		resource.ClusterType:  cls,
		resource.ListenerType: lsnr,
		resource.RouteType:    rds,
	}
	snapshot, err := cache.NewSnapshot(fmt.Sprint(s.serviceSnapVersion), resources)
	if err != nil {
		return fmt.Errorf("Failed to create service snapshot: %v", err)
	}
	err = s.servicesCache.SetSnapshot(ctx, EmptyNodeID, snapshot)
	if err != nil {
		return fmt.Errorf("Failed to set services snapshot %v", err)
	}
	// Sync linear caches
	deltaCLS, deltaVHDS, deltaRDS := servicesToResourcesWithNames(serviceStore, "")
	s.getDeltaCacheForType(resource.ClusterType).SetResources(deltaCLS)
	s.getDeltaCacheForType(resource.VirtualHostType).SetResources(deltaVHDS)
	s.getDeltaCacheForType(resource.RouteType).SetResources(deltaRDS)

	s.nodesMu.Lock()
	defer s.nodesMu.Unlock()
	for nodeID, node := range s.nodes {
		for sID, res := range node.resources {
			for typeURL, resources := range res.servicesNames {
				if err := s.updateNodeStreamServiceResources(nodeID, typeURL, node, sID, resources); err != nil {
					log.Logger.Error("Failed to update service resources before snapping",
						"type", typeURL, "node", nodeID,
						"resources", resources, "stream_id", sID,
						"error", err,
					)
				}
			}
		}
		if err := s.nodeServiceSnapshot(nodeID, node); err != nil {
			log.Logger.Error("Failed to update service snapshot for node", "node", nodeID, "error", err)
		}
	}
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
	s.snapNodesMu.Lock()
	defer s.snapNodesMu.Unlock()
	s.endpointsSnapVersion += int32(1)
	resources := map[string][]types.Resource{
		resource.EndpointType: eds,
	}
	snapshot, err := cache.NewSnapshot(fmt.Sprint(s.endpointsSnapVersion), resources)
	if err != nil {
		return fmt.Errorf("Failed to create endpoints snapshot: %v", err)
	}
	err = s.endpointsCache.SetSnapshot(ctx, EmptyNodeID, snapshot)
	if err != nil {
		return fmt.Errorf("Failed to set endpoints snapshot %v", err)
	}
	// Sync linear caches
	deltaEDS := endpointSlicesToClusterLoadAssignmentsWithNames(endpointStore, "")
	s.getDeltaCacheForType(resource.EndpointType).SetResources(deltaEDS)

	s.nodesMu.Lock()
	defer s.nodesMu.Unlock()
	for nodeID, node := range s.nodes {
		for sID, res := range node.resources {
			for typeURL, resources := range res.endpointsNames {
				if err := s.updateNodeStreamEndpointsResources(nodeID, typeURL, node, sID, resources); err != nil {
					log.Logger.Error("Failed to update endpoints resources before snapping",
						"type", typeURL, "node", nodeID,
						"resources", resources, "stream_id", sID,
						"error", err,
					)
				}
			}
		}
		if err := s.nodeEndpointsSnapshot(nodeID, node); err != nil {
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
	log.Logger.Info("OnDeltaStreamClosed", "id", id)
}

func (s *Snapshotter) OnDeltaStreamOpen(ctx context.Context, id int64, typ string) error {
	var peerAddr string
	if peerInfo, ok := peer.FromContext(ctx); ok {
		peerAddr = peerInfo.Addr.String()
	}
	log.Logger.Info("OnDeltaStreamOpen", "peer address", peerAddr, "id", id, "type", typ)
	return nil
}

func (s *Snapshotter) OnStreamDeltaRequest(id int64, r *discovery.DeltaDiscoveryRequest) error {
	log.Logger.Info("OnStreamDeltaRequest",
		"id", id,
		"received", r.GetTypeUrl(),
		"node", r.GetNode().GetId(),
		"locality", r.GetNode().GetLocality(),
		"subscribes", strings.Join(r.GetResourceNamesSubscribe(), ", "),
		"unsubscribes", strings.Join(r.GetResourceNamesUnsubscribe(), ", "),
		"response_nonce", r.GetResponseNonce(),
	)
	for _, resource := range r.GetResourceNamesSubscribe() {
		resources := s.getDeltaCacheForType(r.GetTypeUrl()).GetResources()
		if _, ok := resources[resource]; !ok {
			log.Logger.Warn("Resource not found in cache", "type", r.GetTypeUrl(), "resource", resource)
		}
	}
	return nil
}

func (s *Snapshotter) OnStreamDeltaResponse(id int64, req *discovery.DeltaDiscoveryRequest, resp *discovery.DeltaDiscoveryResponse) {
	log.Logger.Info("OnStreamDeltaResponse",
		"id", id,
		"type", resp.GetTypeUrl(),
		"resources", len(resp.GetResources()),
	)
}

func (s *Snapshotter) OnFetchResponse(req *discovery.DiscoveryRequest, resp *discovery.DiscoveryResponse) {
	log.Logger.Info("OnFetchResponse")
}

// addOrUpdateNode will add a new node if not present, or add a new stream
// resources placeholder if needed.
func (s *Snapshotter) addOrUpdateNode(nodeID, address string, streamID int64) {
	s.nodesMu.Lock()
	defer s.nodesMu.Unlock()
	if nodeID == EmptyNodeID {
		return
	}
	node, ok := s.nodes[nodeID]
	if !ok {
		// Node not found, add a new one without any resources
		log.Logger.Info("Node cache not found, initialising", "node", nodeID)
		s.nodes[nodeID] = &Node{
			address: address,
			resources: map[int64]*NodeSnapshotResources{
				streamID: makeEmptyNodeResources(),
			},
		}
		return
	}
	if _, exists := node.resources[streamID]; exists {
		return // Stream already know for node
	}
	log.Logger.Info("New stream for node", "node", nodeID, "streamID", streamID)
	node.resources[streamID] = makeEmptyNodeResources()
}

// deleteNodeStream removes a stream from a node's resources and if the list of streams is
// empty deletes the node
func (s *Snapshotter) deleteNodeStream(nodeID string, streamID int64) {
	s.nodesMu.Lock()
	defer s.nodesMu.Unlock()
	if nodeID == EmptyNodeID {
		return
	}
	node, ok := s.nodes[nodeID]
	if !ok {
		log.Logger.Warn("Tried to delete stream for non existing node", "node", nodeID, "stream_id", streamID)
		return
	}
	delete(node.resources, streamID)
	// if no more streams are open, delete the node
	if len(node.resources) == 0 {
		delete(s.nodes, nodeID)
		s.clearNodeCache(nodeID)
		return
	}
	if err := s.nodeServiceSnapshot(nodeID, node); err != nil {
		log.Logger.Warn("Failed to update service snapshot on stream closure", "node", nodeID, "error", err)
	}
	if err := s.nodeEndpointsSnapshot(nodeID, node); err != nil {
		log.Logger.Warn("Failed to update endpoints snapshot on stream closure", "node", nodeID, "error", err)
	}
}

// deleteNode removes a node from nodes map and clears existing snaphots for the node
func (s *Snapshotter) deleteNode(nodeID string) {
	s.nodesMu.Lock()
	defer s.nodesMu.Unlock()
	if nodeID == EmptyNodeID {
		return
	}
	delete(s.nodes, nodeID)
	s.clearNodeCache(nodeID)
}

func (s *Snapshotter) clearNodeCache(nodeID string) {
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

// updateNodeStreamServiceResources updates the list of service resources requested in a node's stream context
// by copying the most up to date version of them from the full snapshot (default snapshot)
// Non-blocking, the caller should lock access to nodes map
func (s *Snapshotter) updateNodeStreamServiceResources(nodeID, typeURL string, node *Node, streamID int64, resources []string) error {
	if _, ok := node.resources[streamID]; !ok {
		return fmt.Errorf("Cannot find service resources to update for node: %s in stream: %d context", nodeID, streamID)
	}

	var newSnapResources []types.Resource
	var err error
	if s.localhostEndpoints {
		newSnapResources, err = makeDummyResources(typeURL, resources)
		if err != nil {
			return fmt.Errorf("Cannot make dummy resources for localhost mode: %s", err)
		}
		log.Logger.Debug("Created dummy resources", "type", typeURL, "resources", newSnapResources, "count", len(newSnapResources))
	} else {
		newSnapResources, err = s.getResourcesFromCache(typeURL, resources)
		if err != nil {
			return fmt.Errorf("Cannot get resources from cache: %s", err)
		}
	}

	node.resources[streamID].services[typeURL] = newSnapResources
	node.resources[streamID].servicesNames[typeURL] = resources
	return nil
}

// updateNodeStreamEndpointsResources updates the list of endpoint resources requested in a node's stream context
// by copying the most up to date version of them from the full snapshot (default snapshot)
// Non-blocking, the caller should lock access to nodes map
func (s *Snapshotter) updateNodeStreamEndpointsResources(nodeID, typeURL string, node *Node, streamID int64, resources []string) error {
	if _, ok := node.resources[streamID]; !ok {
		return fmt.Errorf("Cannot find endpoint resources to update for node: %s in stream: %d context", nodeID, streamID)
	}

	var newSnapResources []types.Resource
	var err error
	if s.localhostEndpoints {
		newSnapResources, err = makeDummyResources(typeURL, resources)
		if err != nil {
			return fmt.Errorf("Cannot make dummy resources for localhost mode: %s", err)
		}
		log.Logger.Debug("Created dummy resources", "type", typeURL, "resources", newSnapResources, "count", len(newSnapResources))
	} else {
		newSnapResources, err = s.getResourcesFromCache(typeURL, resources)
		if err != nil {
			return fmt.Errorf("Cannot get resources from cache: %s", err)
		}
	}

	node.resources[streamID].endpoints[typeURL] = newSnapResources
	node.resources[streamID].endpointsNames[typeURL] = resources
	return nil
}

// nodeServiceSnapshot throws the current service NodeResources content into a new snapshot
func (s *Snapshotter) nodeServiceSnapshot(nodeID string, node *Node) error {
	ctx := context.Background()
	atomic.AddInt32(&node.serviceSnapVersion, 1)
	snapServices := aggregateNodeResources("services", node.resources)
	snapshot, err := cache.NewSnapshot(fmt.Sprint(node.serviceSnapVersion), snapServices)
	if err != nil {
		return err
	}
	return s.servicesCache.SetSnapshot(ctx, nodeID, snapshot)
}

// nodeEndpointsSnapshot throws the current endpoints NodeResources content into a new snapshot
func (s *Snapshotter) nodeEndpointsSnapshot(nodeID string, node *Node) error {
	ctx := context.Background()
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
	s.nodesMu.Lock()
	defer s.nodesMu.Unlock()
	node, ok := s.nodes[nodeID]
	if !ok {
		return fmt.Errorf("Node not found for updating snapshot resources: %s", nodeID)
	}
	if mapTypeURL(typeURL) == "services" {
		if err := s.updateNodeStreamServiceResources(nodeID, typeURL, node, streamID, resources); err != nil {
			return err
		}
		return s.nodeServiceSnapshot(nodeID, node)
	}
	if mapTypeURL(typeURL) == "endpoints" {
		if err := s.updateNodeStreamEndpointsResources(nodeID, typeURL, node, streamID, resources); err != nil {
			return err
		}
		return s.nodeEndpointsSnapshot(nodeID, node)
	}
	return nil
}

// needToUpdateSnapshot checks id a node snapshot needs updating based on the requested resources
// from the client inside a streams context
func (s *Snapshotter) needToUpdateSnapshot(nodeID, typeURL string, streamID int64, resources []string) bool {
	s.nodesMu.RLock()
	defer s.nodesMu.RUnlock()
	node, ok := s.nodes[nodeID]
	if !ok {
		return false
	}
	sNodeResources, ok := node.resources[streamID]
	if !ok {
		log.Logger.Warn("Cannot check if snapshot needs updating, stream not found", "id", streamID)
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
	routeservice.RegisterVirtualHostDiscoveryServiceServer(grpcServer, xdsServer)
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
