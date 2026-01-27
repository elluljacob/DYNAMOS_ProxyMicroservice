//go:build local
// +build local

package main

import "go.uber.org/zap"

var serviceName = "EXT-PROVIDER"

// Change to the desired output log level
var logLevel = zap.DebugLevel

var etcdEndpoints = "http://localhost:30005"

var grpcAddr = "localhost:"
