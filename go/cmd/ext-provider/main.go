package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/Jorrit05/DYNAMOS/pkg/api"
	"github.com/Jorrit05/DYNAMOS/pkg/etcd"
	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	"github.com/Jorrit05/DYNAMOS/pkg/msinit"
	"github.com/gorilla/handlers"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.opencensus.io/plugin/ochttp"
)

var (
	logger      = lib.InitLogger(logLevel)
	COORDINATOR = make(chan struct{})
	port        = ":8080"
	agentConfig lib.AgentDetails // used in register_agent.go
	etcdClient  *clientv3.Client = etcd.GetEtcdClient(etcdEndpoints)
)

func main() {
	// Setup Service Name
	serviceName = os.Getenv("DATA_STEWARD_NAME")
	if serviceName == "" {
		serviceName = "EXT-PROVIDER"
	}

	// Init Tracing
	oce, err := lib.InitTracer(serviceName)
	if err != nil {
		logger.Sugar().Fatalf("Failed to create ocagent-exporter: %v", err)
	}

	logger.Info("Initializing Etcd Client...")
	etcdClient = etcd.GetEtcdClient(etcdEndpoints)
	if etcdClient == nil {
		logger.Fatal("Failed to create Etcd client")
	}

	logger.Debug("Initializing Snowflake Database Connection...")
	if err := InitDB(); err != nil {
		logger.Sugar().Fatalf("Failed to initialize database: %v", err)
	}
	logger.Debug("Database connection established successfully.")

	logger.Debug("Loading policy file...")
	if err := LoadAndLogPolicy(); err != nil {
		logger.Sugar().Fatalf("Failed to load policy: %v", err)
	}
	logger.Debug("Policy file loaded successfully.")

	// Init DYNAMOS/Sidecar Config
	// Note: We pass SidecarHandler from sidecar.go
	config, err := msinit.NewConfiguration(context.Background(), serviceName, grpcAddr, COORDINATOR, SidecarHandler)
	if err != nil {
		logger.Sugar().Fatalf("%v", err)
	}

	// Start HTTP Server
	go startHTTPServer()

	// Register with DYNAMOS
	registerAgent()

	// Block until shutdown
	<-config.StopMicroservice

	config.SafeExit(oce, serviceName)
	os.Exit(0)
}

func startHTTPServer() {
	headersOk := handlers.AllowedHeaders([]string{"X-Requested-With", "Content-Type", "Authorization"})
	originsOk := handlers.AllowedOrigins([]string{"*"})
	methodsOk := handlers.AllowedMethods([]string{"GET", "HEAD", "POST", "PUT", "OPTIONS"})

	agentMux := http.NewServeMux()
	path := fmt.Sprintf("/agent/v1/sqlDataRequest/%s", strings.ToLower(serviceName))

	// Handler from handlers.go
	agentMux.Handle(path, &ochttp.Handler{Handler: HandleSQLRequest()})

	// Middleware from middleware.go
	wrappedMux := AuthMiddleware(agentMux)

	logger.Sugar().Infof("Starting http server on port %s and path %s", port, path)
	if err := http.ListenAndServe(port, api.LogMiddleware(handlers.CORS(originsOk, headersOk, methodsOk)(wrappedMux))); err != nil {
		logger.Sugar().Fatalf("Error starting HTTP server: %v", err)
	}
}
