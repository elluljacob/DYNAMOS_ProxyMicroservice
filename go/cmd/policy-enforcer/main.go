package main

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/eflint"
	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/policyenforcer"
	policyenforcerhttp "github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/policyenforcerhttp"
	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/reasoner"
	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/repository"
	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/service"
	"github.com/Jorrit05/DYNAMOS/pkg/api"
	"github.com/Jorrit05/DYNAMOS/pkg/etcd"
	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"github.com/gorilla/handlers"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

//go:embed eflint/empty.eflint
var embeddedModelFS embed.FS

// Application holds all the dependencies for the policy enforcer service.
type Application struct {
	logger            *zap.Logger
	etcdClient        *clientv3.Client
	grpcConn          *grpc.ClientConn
	rabbitMQClient    pb.RabbitMQClient
	validationService *service.ValidationService
	responseSender    service.ResponseSender
	receiveMutex      *sync.Mutex
}

// NewApplication creates and initializes a new Application with all dependencies.
func NewApplication() (*Application, error) {
	app := &Application{
		logger:       lib.InitLogger(logLevel),
		receiveMutex: &sync.Mutex{},
	}

	// Initialize etcd client
	app.etcdClient = etcd.GetEtcdClient(etcdEndpoints)

	// Initialize gRPC connection and RabbitMQ client
	app.grpcConn = lib.GetGrpcConnection(grpcAddr)
	app.rabbitMQClient = lib.InitializeSidecarMessaging(
		app.grpcConn,
		&pb.InitRequest{
			ServiceName:     fmt.Sprintf("%s-in", serviceName),
			RoutingKey:      fmt.Sprintf("%s-in", serviceName),
			QueueAutoDelete: false,
		},
	)

	// ValidationService will be initialized after eFLINT components are set up
	// See initializeValidationService()

	// Initialize the response sender
	app.responseSender = service.NewRabbitMQResponseSender(app.rabbitMQClient)

	return app, nil
}

// initializeValidationService creates and configures the ValidationService with all strategies.
func (app *Application) initializeValidationService(
	providerConfigRepo repository.ProviderConfigRepository,
	eflintStrategy service.ValidationStrategy,
) {
	legacyStrategy := service.NewLegacyValidationStrategy(
		repository.NewEtcdAgreementRepository(app.etcdClient),
		app.logger,
	)

	app.validationService = service.NewValidationServiceWithConfig(service.ValidationServiceConfig{
		ProviderConfigRepo: providerConfigRepo,
		LegacyStrategy:     legacyStrategy,
		EflintStrategy:     eflintStrategy,
		AuthGenerator:      service.NewStaticAuthTokenGenerator(),
		Logger:             app.logger,
	})
}

// Close cleanly shuts down all resources.
func (app *Application) Close() {
	if app.etcdClient != nil {
		app.etcdClient.Close()
	}
	if app.grpcConn != nil {
		app.grpcConn.Close()
	}
}

// Run starts the application background tasks.
func (app *Application) Run() {
	app.logger.Debug("Starting message consumer")

	go func() {
		lib.StartConsumingWithRetry(
			serviceName,
			app.rabbitMQClient,
			fmt.Sprintf("%s-in", serviceName),
			app.handleIncomingMessages,
			5,
			5*time.Second,
			app.receiveMutex,
		)
	}()
}

func main() {
	_, err := lib.InitTracer(serviceName)
	if err != nil {
		panic(fmt.Sprintf("Failed to create ocagent-exporter: %v", err))
	}

	app, err := NewApplication()
	if err != nil {
		panic(fmt.Sprintf("Failed to initialize application: %v", err))
	}
	defer app.Close()

	app.Run()

	// Resolve the empty model path for bootstrapping pool instances
	modelPath := resolveModelPath(app.logger, eflintModelPath)
	if modelPath == "" {
		app.logger.Warn("eflint model path is empty; pool instances cannot be started")
	}

	managerConfig := &eflint.ManagerConfig{
		EflintServerPath:  eflintServerPath,
		MinPort:           eflintMinPort,
		MaxPort:           eflintMaxPort,
		StartupDelay:      eflintStartupDelay,
		ConnectionTimeout: eflintTimeout,
	}

	// Create the instance pool (replaces the single Manager for validation)
	poolConfig := &eflint.PoolConfig{
		TargetSize:          eflintPoolSize,
		ManagerConfig:       managerConfig,
		EmptyModelPath:      modelPath,
		HealthCheckInterval: eflintHealthCheckInterval,
		AcquireTimeout:      eflintAcquireTimeout,
	}

	pool, err := eflint.NewInstancePool(poolConfig, eflintStateDir, app.logger)
	if err != nil {
		app.logger.Error("failed to create eFLINT instance pool", zap.Error(err))
		panic(fmt.Sprintf("Failed to create eFLINT instance pool: %v", err))
	}

	// Initialize repositories
	providerConfigRepo := repository.NewEtcdProviderConfigRepository(app.etcdClient)
	eflintModelRepo := repository.NewEtcdEflintModelRepository(app.etcdClient)

	// Create the eFLINT reasoner (implements the Reasoner interface, uses pool + model repo)
	eflintReasoner := reasoner.NewEflintReasoner(pool, eflintModelRepo, app.logger)

	// Create eFLINT validation strategy (delegates to the Reasoner)
	eflintStrategy := service.NewEflintValidationStrategy(eflintReasoner, app.logger)

	// Initialize ValidationService with both strategies
	app.initializeValidationService(providerConfigRepo, eflintStrategy)

	app.logger.Info("ValidationService configured with legacy and eFLINT strategies (pool-based)")

	// Create the policy enforcer (uses the Reasoner interface)
	enforcer := policyenforcer.NewEnforcer(eflintReasoner, app.logger)

	// Create a single Manager for the eFLINT debug/management HTTP API endpoints
	defaultManager := eflint.NewManager(managerConfig, app.logger)
	defaultStateManager := eflint.NewStateManager(defaultManager, eflintStateDir, app.logger)

	if autoStartEflint && modelPath != "" {
		app.logger.Info("auto-starting default eFLINT server for HTTP API", zap.String("model", modelPath))
		if err := defaultManager.Start(modelPath); err != nil {
			app.logger.Error("failed to auto-start default eFLINT server", zap.Error(err))
		}
	}

	instanceAPIHandler := eflint.NewInstanceAPIHandler(defaultManager, pool, app.logger)
	stateAPIHandler := eflint.NewStateAPIHandler(defaultStateManager, pool, app.logger)
	policyEnforcerHandler := policyenforcerhttp.NewHTTPHandler(enforcer, app.logger)

	headersOk := handlers.AllowedHeaders([]string{"X-Requested-With", "Content-Type", "Authorization"})
	originsOk := handlers.AllowedOrigins([]string{"*"})
	methodsOk := handlers.AllowedMethods([]string{"GET", "HEAD", "POST", "PUT", "DELETE", "OPTIONS"})

	mux := http.NewServeMux()
	apiMux := http.NewServeMux()

	// Register all routes
	RegisterRoutes(apiMux, instanceAPIHandler, stateAPIHandler, policyEnforcerHandler, pool)

	mux.Handle(apiVersion+"/", http.StripPrefix(apiVersion, apiMux))

	server := &http.Server{
		Addr:    port,
		Handler: api.LogMiddleware(handlers.CORS(originsOk, headersOk, methodsOk)(mux)),
	}

	go func() {
		app.logger.Sugar().Infow("Starting HTTP server", "port", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			app.logger.Sugar().Fatalw("Error starting HTTP server: %s", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	app.logger.Info("shutting down...")

	// Shutdown the pool (stops all instances and health monitor)
	pool.Shutdown()

	// Stop the default manager if running
	if defaultManager.IsRunning() {
		if err := defaultManager.Stop(); err != nil {
			app.logger.Error("failed to stop default eFLINT server", zap.Error(err))
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		app.logger.Error("failed to shutdown HTTP server", zap.Error(err))
	}

	app.logger.Info("shutdown complete")
}

func resolveModelPath(logger *zap.Logger, configuredPath string) string {
	if configuredPath != "" {
		if _, err := os.Stat(configuredPath); err == nil {
			return configuredPath
		} else {
			logger.Warn("configured model path not found, falling back to embedded model",
				zap.String("path", configuredPath),
				zap.Error(err),
			)
		}
	}

	data, err := embeddedModelFS.ReadFile("eflint/empty.eflint")
	if err != nil {
		logger.Warn("failed to read embedded model", zap.Error(err))
		return configuredPath
	}

	tmpFile, err := os.CreateTemp("", "dynamos-agreement-*.eflint")
	if err != nil {
		logger.Warn("failed to create temp file for embedded model", zap.Error(err))
		return configuredPath
	}
	defer tmpFile.Close()

	if _, err := tmpFile.Write(data); err != nil {
		logger.Warn("failed to write embedded model to temp file", zap.Error(err))
		return configuredPath
	}

	return tmpFile.Name()
}
