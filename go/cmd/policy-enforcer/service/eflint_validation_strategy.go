package service

import (
	"context"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/reasoner"
	"go.uber.org/zap"
)

// EflintValidationStrategy validates using the eFLINT policy reasoner.
// It delegates the pool lifecycle and eFLINT interaction to the Reasoner,
// mapping the result to a ValidationResult for the ValidationService.
type EflintValidationStrategy struct {
	reasoner reasoner.Reasoner
	logger   *zap.Logger
}

// NewEflintValidationStrategy creates a new EflintValidationStrategy.
func NewEflintValidationStrategy(
	reasoner reasoner.Reasoner,
	logger *zap.Logger,
) *EflintValidationStrategy {
	return &EflintValidationStrategy{
		reasoner: reasoner,
		logger:   logger,
	}
}

// Name returns the strategy name.
func (s *EflintValidationStrategy) Name() string {
	return "eflint"
}

// ValidateAndPersist validates and saves an eFLINT agreement model.
func (s *EflintValidationStrategy) ValidateAndPersist(ctx context.Context, steward string, payload []byte) error {
	return s.reasoner.ValidateAndPersistModel(ctx, steward, string(payload))
}

// Validate validates a user's access using the eFLINT policy reasoner.
// The reasoner handles the full lifecycle: acquiring a pool instance, loading
// the organization's model, querying facts, and releasing the instance.
func (s *EflintValidationStrategy) Validate(steward, userName string) *ValidationResult {
	clauses, err := s.reasoner.GetAllAllowedClauses(context.Background(), steward, userName)
	if err != nil {
		s.logger.Error("eFLINT validation failed",
			zap.String("steward", steward),
			zap.String("user", userName),
			zap.Error(err),
		)
		return invalidResult(steward, "eFLINT validation error: "+err.Error())
	}

	// Check if user has any allowed archetypes
	if len(clauses.Archetypes) == 0 {
		s.logger.Info("No archetypes allowed for user in eFLINT agreement",
			zap.String("steward", steward),
			zap.String("user", userName),
		)
		return invalidResult(steward, "no matching archetypes in eFLINT agreement")
	}

	s.logger.Debug("eFLINT validation successful",
		zap.String("steward", steward),
		zap.String("user", userName),
		zap.Strings("archetypes", clauses.Archetypes),
		zap.Strings("computeProviders", clauses.ComputeProviders),
	)

	return &ValidationResult{
		Steward:             steward,
		IsValid:             true,
		MatchedArchetypes:   clauses.Archetypes,
		MatchedComputeProvs: clauses.ComputeProviders,
	}
}
