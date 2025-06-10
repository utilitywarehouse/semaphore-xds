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
	EmptyNodeID              = ""
)

// Stream will keep the peer address handy for logging and a rate limiter per stream
type Stream struct {
	peerAddress      string
	requestRateLimit *rate.Limiter
}

type StreamStore struct {
	sync.RWMutex
	streams map[int64]*Stream
}

func NewStreamStore() *StreamStore {
	return &StreamStore{
		streams: make(map[int64]*Stream),
	}
}

type Node struct {
	address              string
	resources            map[int64]*NodeSnapshotResources // map of node resources per open stream id
	serviceSnapVersion   int32                            // Service snap version for node specific cache snapshot
	endpointsSnapVersion int32                            // Endpoints snap version for node specific cache snapshot
}

type NodeStore struct {
	sync.RWMutex
	nodes map[string]*Node
}

func NewNodeStore() *NodeStore {
	return &NodeStore{
		nodes: make(map[string]*Node),
	}
}

// NodeSnapshot keeps resources and versions to help snapshotting per node
type NodeSnapshotResources struct {
	services       map[string][]types.Resource
	servicesNames  map[string][]string
	endpoints      map[string][]types.Resource
	endpointsNames map[string][]string
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
	requestRateLimit       *rate.Limiter // maximum number of requests allowed to server
	streamRequestPerSecond float64       // maximum number of requests per stream per second
	streams                *StreamStore  // map of open streams
	nodes                  *NodeStore    // maps all clients node ids to requested resources
	snapNodesMu            sync.Mutex    // Protects snapshot version increments and cache operations
	localhostEndpoints     bool
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
		requestRateLimit:       rate.NewLimiter(rate.Limit(requestLimit), 1),
		streamRequestPerSecond: streamRequestLimit,
		streams:                NewStreamStore(),
		nodes:                  NewNodeStore(),
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
	s.nodes.RLock()
	defer s.nodes.RUnlock()

	r := make(map[string]string, len(s.nodes.nodes))
	for nodeID, node := range s.nodes.nodes {
		r[nodeID] = node.address
	}
	return r
}

// SnapServices dumps the list of watched Kubernetes Services into services snapshots.
func (s *Snapshotter) SnapServices(serviceStore XdsServiceStore) error {
	// Prepare resources without holding locks
	cls, rds, lsnr, err := servicesToResources(serviceStore, "")
	if err != nil {
		return fmt.Errorf("Failed to snapshot Services: %v", err)
	}

	if s.authority != "" {
		xdstpCLS, xdstpRDS, xdstpLSNR, err := servicesToResources(serviceStore, s.authority)
		if err != nil {
			return fmt.Errorf("Failed to snapshot Services: %v", err)
		}
		cls = append(cls, xdstpCLS...)
		rds = append(rds, xdstpRDS...)
		lsnr = append(lsnr, xdstpLSNR...)
	}

	// Only hold lock during version increment
	s.snapNodesMu.Lock()
	version := atomic.AddInt32(&s.serviceSnapVersion, 1)
	s.snapNodesMu.Unlock()

	resources := map[string][]types.Resource{
		resource.ClusterType:  cls,
		resource.ListenerType: lsnr,
		resource.RouteType:    rds,
	}

	snapshot, err := cache.NewSnapshot(fmt.Sprint(version), resources)
	if err != nil {
		return fmt.Errorf("Failed to create service snapshot: %v", err)
	}

	ctx := context.Background()
	if err := s.servicesCache.SetSnapshot(ctx, EmptyNodeID, snapshot); err != nil {
		return fmt.Errorf("Failed to set services snapshot %v", err)
	}

	// Sync linear caches
	deltaCLS, deltaVHDS, deltaRDS := servicesToResourcesWithNames(serviceStore, "")
	s.getDeltaCacheForType(resource.ClusterType).SetResources(deltaCLS)
	s.getDeltaCacheForType(resource.VirtualHostType).SetResources(deltaVHDS)
	s.getDeltaCacheForType(resource.RouteType).SetResources(deltaRDS)

	// Get list of node IDs to process
	s.nodes.RLock()
	nodeIDs := make([]string, 0, len(s.nodes.nodes))
	for nodeID := range s.nodes.nodes {
		nodeIDs = append(nodeIDs, nodeID)
	}
	s.nodes.RUnlock()

	// Process each node independently
	for _, nodeID := range nodeIDs {
		if err := s.nodeServiceSnapshot(nodeID); err != nil {
			log.Logger.Error("Failed to update service snapshot for node", "node", nodeID, "error", err)
		}
	}
	return nil
}

// SnapEndpoints dumps the list of watched Kubernetes EndpointSlices into endpoints snapshots.
func (s *Snapshotter) SnapEndpoints(endpointStore XdsEndpointStore) error {
	eds, err := endpointSlicesToClusterLoadAssignments(endpointStore, "")
	if err != nil {
		return fmt.Errorf("Failed to snapshot EndpointSlices: %v", err)
	}

	if s.authority != "" {
		xdstpEDS, err := endpointSlicesToClusterLoadAssignments(endpointStore, s.authority)
		if err != nil {
			return fmt.Errorf("Failed to snapshot EndpointSlices: %v", err)
		}
		eds = append(eds, xdstpEDS...)
	}

	// Only hold lock during version increment
	s.snapNodesMu.Lock()
	version := atomic.AddInt32(&s.endpointsSnapVersion, 1)
	s.snapNodesMu.Unlock()

	resources := map[string][]types.Resource{
		resource.EndpointType: eds,
	}

	snapshot, err := cache.NewSnapshot(fmt.Sprint(version), resources)
	if err != nil {
		return fmt.Errorf("Failed to create endpoints snapshot: %v", err)
	}

	ctx := context.Background()
	if err := s.endpointsCache.SetSnapshot(ctx, EmptyNodeID, snapshot); err != nil {
		return fmt.Errorf("Failed to set endpoints snapshot %v", err)
	}

	// Sync linear caches
	deltaEDS := endpointSlicesToClusterLoadAssignmentsWithNames(endpointStore, "")
	s.getDeltaCacheForType(resource.EndpointType).SetResources(deltaEDS)

	// Get list of node IDs to process
	s.nodes.RLock()
	nodeIDs := make([]string, 0, len(s.nodes.nodes))
	for nodeID := range s.nodes.nodes {
		nodeIDs = append(nodeIDs, nodeID)
	}
	s.nodes.RUnlock()

	// Process each node independently
	for _, nodeID := range nodeIDs {
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

	s.streams.Lock()
	defer s.streams.Unlock()
	s.streams.streams[id] = &Stream{
		peerAddress:      peerAddr,
		requestRateLimit: rate.NewLimiter(rate.Limit(s.streamRequestPerSecond), 1),
	}

	metricOnStreamOpenInc()
	return nil
}

func (s *Snapshotter) OnStreamClosed(id int64, node *core.Node) {
	log.Logger.Info("OnStreamClosed", "id", id, "node", node)

	s.streams.Lock()
	delete(s.streams.streams, id)
	s.streams.Unlock()

	s.deleteNodeStream(node.GetId(), id)
	metricOnStreamClosedInc()
}

func (s *Snapshotter) OnStreamRequest(id int64, r *discovery.DiscoveryRequest) error {
	ctx := context.Background()

	s.streams.RLock()
	stream, ok := s.streams.streams[id]
	s.streams.RUnlock()
	if !ok {
		return status.Errorf(codes.NotFound, "stream not found")
	}

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
	s.nodes.Lock()
	defer s.nodes.Unlock()

	node, exists := s.nodes.nodes[nodeID]
	if !exists {
		// Node not found, add a new one without any resources
		log.Logger.Info("Node cache not found, initialising", "node", nodeID)
		s.nodes.nodes[nodeID] = &Node{
			address: address,
			resources: map[int64]*NodeSnapshotResources{
				streamID: makeEmptyNodeResources(),
			},
		}
		return
	}

	// Check if stream already exists
	if _, exists := node.resources[streamID]; exists {
		return
	}

	log.Logger.Info("New stream for node", "node", nodeID, "streamID", streamID)

	// Create a new node with updated resources
	newNode := *node
	newNode.resources = deepCopyNodeResources(node.resources)
	newNode.resources[streamID] = makeEmptyNodeResources()
	s.nodes.nodes[nodeID] = &newNode
}

// deleteNodeStream removes a stream from a node's resources and if the list of streams is
// empty deletes the node
func (s *Snapshotter) deleteNodeStream(nodeID string, streamID int64) {
	if nodeID == EmptyNodeID {
		return
	}

	s.nodes.Lock()
	defer s.nodes.Unlock()

	node, exists := s.nodes.nodes[nodeID]
	if !exists {
		log.Logger.Warn("Tried to delete stream for non existing node", "node", nodeID, "stream_id", streamID)
		return
	}

	// Create a new node with updated resources
	newNode := *node
	newNode.resources = deepCopyNodeResources(node.resources)
	delete(newNode.resources, streamID)

	// if no more streams are open, delete the node
	if len(newNode.resources) == 0 {
		delete(s.nodes.nodes, nodeID)
		s.deleteNodeSnapshots(nodeID)
		return
	}

	// else just update the node
	s.nodes.nodes[nodeID] = &newNode

	// Update snapshots without holding the nodes lock
	go func() {
		if err := s.nodeServiceSnapshot(nodeID); err != nil {
			log.Logger.Warn("Failed to update service snapshot on stream closure", "node", nodeID, "error", err)
		}
		if err := s.nodeEndpointsSnapshot(nodeID); err != nil {
			log.Logger.Warn("Failed to update endpoints snapshot on stream closure", "node", nodeID, "error", err)
		}
	}()
}

// deleteNodeSnapshots removes a node's snapshots (called with nodes lock held)
func (s *Snapshotter) deleteNodeSnapshots(nodeID string) {
	s.snapNodesMu.Lock()
	defer s.snapNodesMu.Unlock()
	s.servicesCache.ClearSnapshot(nodeID)
	s.endpointsCache.ClearSnapshot(nodeID)
}

// getResourcesFromCache returns a set of resources from the full resources snapshot
func (s *Snapshotter) getResourcesFromCache(typeURL string, resources []string) ([]types.Resource, error) {
	var fullResourcesSnap cache.ResourceSnapshot
	var err error

	switch mapTypeURL(typeURL) {
	case "services":
		fullResourcesSnap, err = s.servicesCache.GetSnapshot(EmptyNodeID)
	case "endpoints":
		fullResourcesSnap, err = s.endpointsCache.GetSnapshot(EmptyNodeID)
	default:
		return nil, fmt.Errorf("Unknown type URL: %s", typeURL)
	}

	if err != nil {
		return nil, fmt.Errorf("Cannot get full resources snapshot from cache: %v", err)
	}

	fullResources := fullResourcesSnap.GetResources(typeURL)
	res := make([]types.Resource, 0, len(resources))

	for _, name := range resources {
		r, ok := fullResources[name]
		if !ok {
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
func (s *Snapshotter) updateNodeStreamServiceResources(nodeID, typeURL string, streamID int64, resources []string) error {
	s.nodes.Lock()
	defer s.nodes.Unlock()

	node, exists := s.nodes.nodes[nodeID]
	if !exists {
		return fmt.Errorf("Cannot update service snapshot resources, node: %s not found", nodeID)
	}

	if _, exists := node.resources[streamID]; !exists {
		return fmt.Errorf("Cannot find service resources to update for node: %s in stream: %d context", nodeID, streamID)
	}

	var newSnapResources []types.Resource
	var err error

	if s.localhostEndpoints {
		newSnapResources, err = makeDummyResources(typeURL, resources)
		if err != nil {
			return fmt.Errorf("Cannot make dummy resources for localhost mode: %v", err)
		}
		log.Logger.Debug("Created dummy resources", "type", typeURL, "resources", newSnapResources, "count", len(newSnapResources))
	} else {
		newSnapResources, err = s.getResourcesFromCache(typeURL, resources)
		if err != nil {
			return fmt.Errorf("Cannot get resources from cache: %v", err)
		}
	}

	// Create a new node with updated resources
	newNode := *node
	newNode.resources = deepCopyNodeResources(node.resources)
	newNode.resources[streamID].services[typeURL] = newSnapResources
	newNode.resources[streamID].servicesNames[typeURL] = resources

	s.nodes.nodes[nodeID] = &newNode
	log.Logger.Debug("Updating node resources", "node", nodeID, "type", typeURL, "resources", newNode.resources[streamID].services[typeURL])
	return nil
}

// updateNodeStreamEndpointsResources updates the list of endpoint resources requested in a node's stream context
func (s *Snapshotter) updateNodeStreamEndpointsResources(nodeID, typeURL string, streamID int64, resources []string) error {
	s.nodes.Lock()
	defer s.nodes.Unlock()

	node, exists := s.nodes.nodes[nodeID]
	if !exists {
		return fmt.Errorf("Cannot update endpoint snapshot resources, node: %s not found", nodeID)
	}

	if _, exists := node.resources[streamID]; !exists {
		return fmt.Errorf("Cannot find endpoint resources to update for node: %s in stream: %d context", nodeID, streamID)
	}

	var newSnapResources []types.Resource
	var err error

	if s.localhostEndpoints {
		newSnapResources, err = makeDummyResources(typeURL, resources)
		if err != nil {
			return fmt.Errorf("Cannot make dummy resources for localhost mode: %v", err)
		}
		log.Logger.Debug("Created dummy resources", "type", typeURL, "resources", newSnapResources, "count", len(newSnapResources))
	} else {
		newSnapResources, err = s.getResourcesFromCache(typeURL, resources)
		if err != nil {
			return fmt.Errorf("Cannot get resources from cache: %v", err)
		}
	}

	// Create a new node with updated resources
	newNode := *node
	newNode.resources = deepCopyNodeResources(node.resources)
	newNode.resources[streamID].endpoints[typeURL] = newSnapResources
	newNode.resources[streamID].endpointsNames[typeURL] = resources

	s.nodes.nodes[nodeID] = &newNode
	return nil
}

// nodeServiceSnapshot creates a new service snapshot for a node
func (s *Snapshotter) nodeServiceSnapshot(nodeID string) error {
	s.nodes.RLock()
	node, exists := s.nodes.nodes[nodeID]
	s.nodes.RUnlock()

	if !exists {
		return fmt.Errorf("Cannot create a new snapshot, node: %s not found", nodeID)
	}

	snapServices := aggregateNodeResources("services", node.resources)
	version := atomic.AddInt32(&node.serviceSnapVersion, 1)

	snapshot, err := cache.NewSnapshot(fmt.Sprint(version), snapServices)
	if err != nil {
		return err
	}

	ctx := context.Background()
	return s.servicesCache.SetSnapshot(ctx, nodeID, snapshot)
}

// nodeEndpointsSnapshot creates a new endpoints snapshot for a node
func (s *Snapshotter) nodeEndpointsSnapshot(nodeID string) error {
	s.nodes.RLock()
	node, exists := s.nodes.nodes[nodeID]
	s.nodes.RUnlock()

	if !exists {
		return fmt.Errorf("Cannot create a new snapshot, node: %s not found", nodeID)
	}

	snapEndpoints := aggregateNodeResources("endpoints", node.resources)
	version := atomic.AddInt32(&node.endpointsSnapVersion, 1)

	snapshot, err := cache.NewSnapshot(fmt.Sprint(version), snapEndpoints)
	if err != nil {
		return err
	}

	ctx := context.Background()
	return s.endpointsCache.SetSnapshot(ctx, nodeID, snapshot)
}

// updateStreamNodeResources updates the resources tracked for the node inside a streams context
func (s *Snapshotter) updateStreamNodeResources(nodeID, typeURL string, streamID int64, resources []string) error {
	switch mapTypeURL(typeURL) {
	case "services":
		if err := s.updateNodeStreamServiceResources(nodeID, typeURL, streamID, resources); err != nil {
			return err
		}
		return s.nodeServiceSnapshot(nodeID)
	case "endpoints":
		if err := s.updateNodeStreamEndpointsResources(nodeID, typeURL, streamID, resources); err != nil {
			return err
		}
		return s.nodeEndpointsSnapshot(nodeID)
	default:
		return fmt.Errorf("Unknown type URL: %s", typeURL)
	}
}

// needToUpdateSnapshot checks if a node snapshot needs updating based on the requested resources
func (s *Snapshotter) needToUpdateSnapshot(nodeID, typeURL string, streamID int64, resources []string) bool {
	s.nodes.RLock()
	defer s.nodes.RUnlock()

	node, exists := s.nodes.nodes[nodeID]
	if !exists {
		return false
	}

	sNodeResources, exists := node.resources[streamID]
	if !exists {
		log.Logger.Warn("Cannot check if snapshot needs updating, stream not found", "id", streamID)
		return false
	}

	switch mapTypeURL(typeURL) {
	case "services":
		return !resourcesMatch(sNodeResources.servicesNames[typeURL], resources)
	case "endpoints":
		return !resourcesMatch(sNodeResources.endpointsNames[typeURL], resources)
	default:
		return false
	}
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
		return nil
	}
	wait, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return requestRateLimit.Wait(wait)
}

// aggregateNodeResources places all resources requested under different node streams in
// the same list for creating a new snapshot
func aggregateNodeResources(resourceType string, nodeResources map[int64]*NodeSnapshotResources) map[string][]types.Resource {
	aggrResources := make(map[string][]types.Resource)

	for _, r := range nodeResources {
		var resources map[string][]types.Resource
		if resourceType == "services" {
			resources = r.services
		} else if resourceType == "endpoints" {
			resources = r.endpoints
		} else {
			continue
		}

		for typeUrl, res := range resources {
			aggrResources[typeUrl] = append(aggrResources[typeUrl], res...)
		}
	}
	return aggrResources
}

func makeEmptyNodeResources() *NodeSnapshotResources {
	return &NodeSnapshotResources{
		services: map[string][]types.Resource{
			resource.ClusterType:  {},
			resource.ListenerType: {},
			resource.RouteType:    {},
		},
		servicesNames: map[string][]string{
			resource.ClusterType:  {},
			resource.ListenerType: {},
			resource.RouteType:    {},
		},
		endpoints: map[string][]types.Resource{
			resource.EndpointType: {},
		},
		endpointsNames: map[string][]string{
			resource.EndpointType: {},
		},
	}
}
