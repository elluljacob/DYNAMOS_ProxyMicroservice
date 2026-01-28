package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/Jorrit05/DYNAMOS/pkg/api"
	"github.com/Jorrit05/DYNAMOS/pkg/etcd"
	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	"github.com/Jorrit05/DYNAMOS/pkg/msinit"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"github.com/gorilla/handlers"
	_ "github.com/snowflakedb/gosnowflake"
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

// messageHandler remains largely the same, waiting for the HTTP trigger
func messageHandler(config *msinit.Configuration) func(ctx context.Context, msComm *pb.MicroserviceCommunication) error {
	return func(ctx context.Context, msComm *pb.MicroserviceCommunication) error {
		ctx, span, err := lib.StartRemoteParentSpan(ctx, serviceName+"/func: messageHandler", msComm.Traces)
		if err != nil {
			logger.Sugar().Warnf("Error starting span: %v", err)
		}
		defer span.End()

		// Logic waits here until simpleLogHandler is called
		<-COORDINATOR

		switch msComm.RequestType {
		case "sqlDataRequest":
			logger.Debug("[EXT_PROVIDER] Received request from user, proceeding to forward.")
		case "compositionRequest":
			logger.Debug("Received compositionRequest - Setting up environment")
		default:
			logger.Sugar().Warnf("Unknown RequestType: %v", msComm.RequestType)
		}

		config.NextClient.SendData(ctx, msComm)
		close(config.StopMicroservice)
		return nil
	}
}

func main() {
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

	go func() {
		headersOk := handlers.AllowedHeaders([]string{"X-Requested-With", "Content-Type", "Authorization"})
		originsOk := handlers.AllowedOrigins([]string{"*"})
		methodsOk := handlers.AllowedMethods([]string{"GET", "HEAD", "POST", "PUT", "OPTIONS"})

		agentMux := http.NewServeMux()
		path := fmt.Sprintf("/agent/v1/sqlDataRequest/%s", strings.ToLower(serviceName))

		agentMux.Handle(path, &ochttp.Handler{Handler: simpleLogHandler()})
		wrappedMux := authMiddleware(agentMux)

		logger.Sugar().Infof("Starting http server on port %s and path %s", port, path)
		if err := http.ListenAndServe(port, api.LogMiddleware(handlers.CORS(originsOk, headersOk, methodsOk)(wrappedMux))); err != nil {
			logger.Sugar().Fatalf("Error starting HTTP server: %v", err)
		}
	}()

	registerAgent()
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

// Add this struct at the top of your file or inside the handler
type DataRequest struct {
	Query string `json:"query"`
}

func simpleLogHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Sugar().Infof("[HTTP] %s request on %s", r.Method, r.URL.Path)

		body, _ := io.ReadAll(r.Body)

		// --- NEW: Parse the JSON body ---
		var req DataRequest
		// We use a generic map or a specific struct to get the nested query
		// Based on your logs, the 'query' is at the top level of the POST body
		if err := json.Unmarshal(body, &req); err != nil {
			logger.Sugar().Errorf("Failed to parse JSON body: %v", err)
			// Fallback to raw body if JSON fails
			req.Query = string(body)
		}

		if req.Query == "" {
			req.Query = "SELECT COUNT(*) FROM EMPLOYEES"
		}

		logger.Sugar().Infof("[HTTP] Extracted SQL Query: %s", req.Query)

		// --- Snowflake Connection ---
		dsn := "user:pass@10.40.11.218:9090/TEST_DB/PUBLIC?account=test&protocol=http"
		db, err := sql.Open("snowflake", dsn)
		if err != nil {
			logger.Sugar().Errorf("DB Connection Error: %v", err)
			w.Write([]byte(fmt.Sprintf("Error: %v", err)))
			return
		}
		defer db.Close()

		rows, err := db.Query(req.Query)
		if err != nil {
			logger.Sugar().Errorf("Query Execution Error: %v", err)
			w.Write([]byte(fmt.Sprintf("SQL Error: %v", err)))
			return
		}
		defer rows.Close()

		var result strings.Builder
		for rows.Next() {
			var val interface{}
			if err := rows.Scan(&val); err != nil {
				continue
			}
			result.WriteString(fmt.Sprintf("%v", val))
		}

		// Trigger coordinator
		select {
		case <-COORDINATOR:
		default:
			close(COORDINATOR)
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(result.String()))
	}
}
