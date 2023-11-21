package kube

import (
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/watch"
)

var (
	kubeWatcherObjects = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "semaphore_xds_kube_watcher_objects",
		Help: "Number of objects watched, by watcher and kind",
	},
		[]string{"kind"},
	)
	kubeWatcherEvents = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "semaphore_xds_kube_watcher_events_total",
		Help: "Number of events handled, by watcher, kind and event_type",
	},
		[]string{"kind", "event_type"},
	)
)

func init() {
	prometheus.MustRegister(
		kubeWatcherObjects,
		kubeWatcherEvents,
	)
}

func metricIncKubeWatcherEvents(kind string, eventType watch.EventType) {
	kubeWatcherEvents.With(prometheus.Labels{
		"kind":       kind,
		"event_type": string(eventType),
	}).Inc()
}

func metricSetKubeWatcherObjects(kind string, v float64) {
	kubeWatcherObjects.With(prometheus.Labels{
		"kind": kind,
	}).Set(v)
}
