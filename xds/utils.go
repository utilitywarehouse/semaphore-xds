package xds

import (
	"fmt"
	"net"
	"strconv"
)

func makeClusterName(name, namespace string, port int32) string {
	//return net.JoinHostPort(fmt.Sprintf("%s.%s", name, namespace), strconv.Itoa(int(port)))
	return fmt.Sprintf("%s.%s.%s", name, namespace, strconv.Itoa(int(port)))
}

// This is a bit confusing but it seems simpler to name the listener, route and
//  virtual host as the service domain we expect to hit from the client.
func makeListenerName(name, namespace string, port int32) string {
	return makeGlobalServiceDomain(name, namespace, port)
}

func makeRouteConfigName(name, namespace string, port int32) string {
	return makeGlobalServiceDomain(name, namespace, port)
}

func makeVirtualHostName(name, namespace string, port int32) string {
	return makeGlobalServiceDomain(name, namespace, port)
}

func makeGlobalServiceDomain(name, namespace string, port int32) string {
	return net.JoinHostPort(fmt.Sprintf("%s.%s", name, namespace), strconv.Itoa(int(port)))
}
