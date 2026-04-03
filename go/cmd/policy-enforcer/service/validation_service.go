package service

import (
	"context"
	"sync"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/repository"
	"github.com/Jorrit05/DYNAMOS/pkg/api"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"go.uber.org/zap"
)

// ValidationService orchestrates the validation of request approvals.
// It uses the Strategy pattern to delegate validation to the appropriate
// strategy based on provider configuration.
type ValidationService struct {
	// Strategy resolution
	providerConfigRepo repository.ProviderConfigRepository
	legacyStrategy     ValidationStrategy
	eflintStrategy     ValidationStrategy

	// Common dependencies
	authGenerator AuthTokenGenerator
	logger        *zap.Logger
}

// ValidationServiceConfig holds the configuration for creating a ValidationService.
type ValidationServiceConfig struct {
	ProviderConfigRepo repository.ProviderConfigRepository
	LegacyStrategy     ValidationStrategy
	EflintStrategy     ValidationStrategy // Optional: nil if eFLINT not configured
	AuthGenerator      AuthTokenGenerator
	Logger             *zap.Logger
}

// NewValidationServiceWithConfig creates a ValidationService with the given configuration.
func NewValidationServiceWithConfig(cfg ValidationServiceConfig) *ValidationService {
	return &ValidationService{
		providerConfigRepo: cfg.ProviderConfigRepo,
		legacyStrategy:     cfg.LegacyStrategy,
		eflintStrategy:     cfg.EflintStrategy,
		authGenerator:      cfg.AuthGenerator,
		logger:             cfg.Logger,
	}
}

// ValidateRequest processes a request approval and returns a validation response.
func (s *ValidationService) ValidateRequest(ctx context.Context, request *pb.RequestApproval) *pb.ValidationResponse {
	s.logger.Debug("Starting request validation",
		zap.String("user", request.User.UserName),
		zap.Strings("dataProviders", request.DataProviders),
	)

	// Build the initial response
	response := s.buildInitialResponse(request)

	// Validate agreements for all requested data providers
	validationResults := s.validateDataProviders(request.DataProviders, request.User.UserName)

	// Process validation results and update response
	s.processValidationResults(validationResults, request.User.UserName, response)

	// Determine if request is approved
	response.RequestApproved = len(response.ValidDataproviders) > 0

	// Generate auth token if approved
	if response.RequestApproved {
		response.Auth = s.authGenerator.GenerateToken()
	}

	s.logger.Info("Request validation completed",
		zap.String("user", request.User.UserName),
		zap.Bool("approved", response.RequestApproved),
		zap.Int("validProviders", len(response.ValidDataproviders)),
		zap.Int("invalidProviders", len(response.InvalidDataproviders)),
	)

	return response
}

// buildInitialResponse creates the initial ValidationResponse with base fields.
func (s *ValidationService) buildInitialResponse(request *pb.RequestApproval) *pb.ValidationResponse {
	response := &pb.ValidationResponse{
		Type:        "validationResponse",
		RequestType: request.Type,
		User: &pb.User{
			Id:       request.User.Id,
			UserName: request.User.UserName,
		},
		RequestApproved:      false,
		ValidArchetypes:      &pb.UserArchetypes{Archetypes: make(map[string]*pb.UserAllowedArchetypes)},
		Options:              make(map[string]bool),
		ValidDataproviders:   make(map[string]*pb.DataProvider),
		InvalidDataproviders: []string{},
	}

	// Copy options from request if present
	if len(request.Options) > 0 {
		response.Options = request.Options
	}

	return response
}

// validateDataProviders validates agreements for all requested data providers concurrently.
// Each provider validation acquires its own pool instance, enabling parallel validation.
func (s *ValidationService) validateDataProviders(dataProviders []string, userName string) []*ValidationResult {
	results := make([]*ValidationResult, len(dataProviders))

	var wg sync.WaitGroup
	for i, steward := range dataProviders {
		wg.Add(1)
		go func(idx int, st string) {
			defer wg.Done()
			results[idx] = s.validateSingleProvider(st, userName)
		}(i, steward)
	}
	wg.Wait()

	return results
}

// validateSingleProvider validates a single data provider using the appropriate strategy.
func (s *ValidationService) validateSingleProvider(steward, userName string) *ValidationResult {
	strategy := s.resolveStrategy(steward)

	s.logger.Debug("Using validation strategy",
		zap.String("steward", steward),
		zap.String("strategy", strategy.Name()),
	)

	return strategy.Validate(steward, userName)
}

// resolveStrategy determines which validation strategy to use for a provider.
func (s *ValidationService) resolveStrategy(steward string) ValidationStrategy {
	// Default to legacy if no provider config repo
	if s.providerConfigRepo == nil {
		return s.legacyStrategy
	}

	config, found, err := s.providerConfigRepo.GetProviderConfig(steward)
	if err != nil {
		s.logger.Warn("Failed to retrieve provider config, defaulting to legacy validation",
			zap.String("steward", steward),
			zap.Error(err),
		)
		return s.legacyStrategy
	}

	if !found {
		return s.legacyStrategy
	}

	// Use eFLINT strategy if configured and available
	if config.ValidationStrategy == api.ValidationStrategyEflint && s.eflintStrategy != nil {
		return s.eflintStrategy
	}

	return s.legacyStrategy
}

// processValidationResults updates the response based on validation results.
func (s *ValidationService) processValidationResults(
	results []*ValidationResult,
	userName string,
	response *pb.ValidationResponse,
) {
	for _, result := range results {
		if result.IsValid {
			s.addValidProvider(result, userName, response)
		} else {
			response.InvalidDataproviders = append(response.InvalidDataproviders, result.Steward)
			s.logger.Debug("Provider validation failed",
				zap.String("steward", result.Steward),
				zap.String("reason", result.InvalidReason),
			)
		}
	}
}

// addValidProvider adds a validated provider to the response.
func (s *ValidationService) addValidProvider(
	result *ValidationResult,
	userName string,
	response *pb.ValidationResponse,
) {
	// Set valid archetypes for user
	response.ValidArchetypes.UserName = userName
	response.ValidArchetypes.Archetypes[result.Steward] = &pb.UserAllowedArchetypes{
		Archetypes: result.MatchedArchetypes,
	}

	// Add to valid data providers
	response.ValidDataproviders[result.Steward] = &pb.DataProvider{
		Archetypes:       result.MatchedArchetypes,
		ComputeProviders: result.MatchedComputeProvs,
	}
}

// ValidateAndPersistAgreement resolves the strategy and delegates validation and persistence.
func (s *ValidationService) ValidateAndPersistAgreement(ctx context.Context, provider string, payload []byte) error {
	strategy := s.resolveStrategy(provider)
	s.logger.Info("Validating and persisting agreement",
		zap.String("provider", provider),
		zap.String("strategy", strategy.Name()),
	)

	return strategy.ValidateAndPersist(ctx, provider, payload)
}
