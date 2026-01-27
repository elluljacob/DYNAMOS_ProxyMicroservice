package main

import (
	"context"
	"fmt"
	"io"
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

	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
)

var (
	logger      = lib.InitLogger(logLevel)
	COORDINATOR = make(chan struct{})

	port        = ":8080"
	agentConfig lib.AgentDetails
	etcdClient  *clientv3.Client = etcd.GetEtcdClient(etcdEndpoints)
)

func messageHandler(config *msinit.Configuration) func(ctx context.Context, msComm *pb.MicroserviceCommunication) error {
	return func(ctx context.Context, msComm *pb.MicroserviceCommunication) error {
		ctx, span, err := lib.StartRemoteParentSpan(ctx, serviceName+"/func: messageHandler", msComm.Traces)
		if err != nil {
			logger.Sugar().Warnf("Error starting span: %v", err)
		}
		defer span.End()
		// Wait till all services are ready
		<-COORDINATOR

		switch msComm.RequestType {
		case "sqlDataRequest": // Using your example request type
			logger.Debug("[EXT_PROVIDER] Received request from user")
			logger.Debug("[EXT_PROVIDER] Simulating data retrieval for query")

			// Logic: Here you would call your external API.
			// For now, we just log and move on.
		case "compositionRequest":
			logger.Debug("Received compositionRequest - Setting up environment")
			// compositionRequest := &pb.CompositionRequest{}
			// grpcMsg.Body.UnmarshalTo(compositionRequest)

			// // 1. Register the job so the system knows we are ready
			// registerUserWithJob(ctx, compositionRequest)

			// 2. Create the local queue for this job
			// In the real code, this is localJobname (e.g., jacob-test...ext_provider1)
			// handleQueue(ctx, compositionRequest.JobName, compositionRequest.LocalJobName, ...)

		default:
			logger.Sugar().Warnf("Unknown RequestType: %v", msComm.RequestType)
		}

		// Forward to the next service in the chain
		config.NextClient.SendData(ctx, msComm)
		close(config.StopMicroservice)
		return nil
	}
}

func main() {
	logger.Sugar().Infof("(DEBUG) Log level: %v, addr: %s", logLevel, grpcAddr)
	logger.Sugar().Infof("Using latest version")
	logger.Sugar().Debugf("Starting %s service", serviceName)
	serviceName = os.Getenv("DATA_STEWARD_NAME")
	if serviceName == "" {
		serviceName = "EXT-PROVIDER"
	}

	oce, err := lib.InitTracer(serviceName)
	if err != nil {
		logger.Sugar().Fatalf("Failed to create ocagent-exporter: %v", err)
	}

	config, err := msinit.NewConfiguration(context.Background(), serviceName, grpcAddr, COORDINATOR, messageHandler)
	if err != nil {
		logger.Sugar().Fatalf("%v", err)
	}

	logger.Sugar().Info("Registering agent with etcd...")

	go func() {
		headersOk := handlers.AllowedHeaders([]string{"X-Requested-With", "Content-Type", "Authorization"})
		originsOk := handlers.AllowedOrigins([]string{"*"})
		methodsOk := handlers.AllowedMethods([]string{"GET", "HEAD", "POST", "PUT", "OPTIONS"})

		agentMux := http.NewServeMux()
		// Path: /agent/v1/sqlDataRequest/ext-provider
		path := fmt.Sprintf("/agent/v1/sqlDataRequest/%s", strings.ToLower(serviceName))

		// Use the simplified logger handler you asked for
		agentMux.Handle(path, &ochttp.Handler{Handler: simpleLogHandler()})

		// Wrap with the auth middleware you provided
		wrappedMux := authMiddleware(agentMux)

		logger.Sugar().Infof("Starting http server on port %s and path %s", port, path)
		if err := http.ListenAndServe(port, api.LogMiddleware(handlers.CORS(originsOk, headersOk, methodsOk)(wrappedMux))); err != nil {
			logger.Sugar().Fatalf("Error starting HTTP server: %v", err)
		}
	}()
	// --- HTTP SERVER LOGIC END ---

	logger.Sugar().Info("Registering agent with etcd...")
	registerAgent()

	// Wait until the workflow finishes
	<-config.StopMicroservice

	config.SafeExit(oce, serviceName)
	os.Exit(0)
}

func handleIncomingMessages(ctx context.Context, grpcMsg *pb.SideCarMessage) error {
	logger.Debug("Received message via Sidecar/RabbitMQ")
	// For now, just log that we got something
	return nil
}

func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Debug("Entering authMiddleware")
		// For now, we just pass through or check for the existence of the header
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			logger.Warn("Authorization header is missing - providing access for debug")
		}
		next.ServeHTTP(w, r)
	})
}

func simpleLogHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Sugar().Infof("[HTTP] %s request on %s", r.Method, r.URL.Path)

		// If it's a GET, we won't have a body!
		if r.Method == http.MethodGet {
			logger.Sugar().Warn("Received GET request, but expected POST with data")
		}

		body, _ := io.ReadAll(r.Body) // Standard library way to double-check
		logger.Sugar().Infof("[HTTP] Raw Body Length: %d characters", len(body))
		logger.Sugar().Infof("[HTTP] Request Body: %s", string(body))

		// Trigger the coordinator so the gRPC side continues
		select {
		case <-COORDINATOR:
		default:
			close(COORDINATOR)
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Received"))
	}
}
