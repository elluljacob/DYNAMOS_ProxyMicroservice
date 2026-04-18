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
	agentConfig lib.AgentDetails
	etcdClient  *clientv3.Client = etcd.GetEtcdClient(etcdEndpoints)
)

func main() {
	serviceName = os.Getenv("DATA_STEWARD_NAME")
	if serviceName == "" {
		serviceName = "EXT-PROVIDER"
	}

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

	logger.Debug("Loading policy from etcd...")
	if err := LoadPolicy(context.Background(), etcdClient); err != nil {
		logger.Sugar().Fatalf("Failed to load policy: %v", err)
	}
	logger.Debug("Policy loaded successfully.")

	config, err := msinit.NewConfiguration(context.Background(), serviceName, grpcAddr, COORDINATOR, SidecarHandler)
	if err != nil {
		logger.Sugar().Fatalf("%v", err)
	}

	go startHTTPServer()
	registerAgent()

	<-config.StopMicroservice
	config.SafeExit(oce, serviceName)
	os.Exit(0)
}

func startHTTPServer() {
	headersOk := handlers.AllowedHeaders([]string{"X-Requested-With", "Content-Type", "Authorization"})
	originsOk := handlers.AllowedOrigins([]string{"*"})
	methodsOk := handlers.AllowedMethods([]string{"GET", "HEAD", "POST", "PUT", "OPTIONS"})

	agentMux := http.NewServeMux()

	sqlPath := fmt.Sprintf("/agent/v1/sqlDataRequest/%s", strings.ToLower(serviceName))
	pythonPath := fmt.Sprintf("/agent/v1/pythonDataRequest/%s", strings.ToLower(serviceName))

	agentMux.Handle(sqlPath, &ochttp.Handler{Handler: HandleSQLRequest()})
	agentMux.Handle(pythonPath, &ochttp.Handler{Handler: HandleSQLRequest()}) // same handler, type detected from body

	wrappedMux := AuthMiddleware(agentMux)

	logger.Sugar().Infof("Starting http server on port %s, paths: %s, %s", port, sqlPath, pythonPath)
	if err := http.ListenAndServe(port, api.LogMiddleware(handlers.CORS(originsOk, headersOk, methodsOk)(wrappedMux))); err != nil {
		logger.Sugar().Fatalf("Error starting HTTP server: %v", err)
	}
}
