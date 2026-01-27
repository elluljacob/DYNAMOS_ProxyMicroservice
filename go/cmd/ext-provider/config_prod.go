//go:build !local
// +build !local

package main

import "go.uber.org/zap"

var logLevel = zap.DebugLevel

// Change to name of your service
var serviceName = "EXT-PROVIDER"

var etcdEndpoints = "http://etcd-0.etcd-headless.core.svc.cluster.local:2379,http://etcd-1.etcd-headless.core.svc.cluster.local:2379,http://etcd-2.etcd-headless.core.svc.cluster.local:2379"

var grpcAddr = "localhost:"
var tracingHost = "collector.linkerd-jaeger:55678"
