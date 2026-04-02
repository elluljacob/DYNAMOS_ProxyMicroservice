package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/repository"
	"github.com/Jorrit05/DYNAMOS/pkg/api"
	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	"go.uber.org/zap"
)

// LegacyValidationStrategy validates using JSON agreements stored in etcd.
// This is the original validation approach using static agreement definitions.
type LegacyValidationStrategy struct {
	agreementRepo repository.AgreementRepository
	logger        *zap.Logger
}

// NewLegacyValidationStrategy creates a new LegacyValidationStrategy.
func NewLegacyValidationStrategy(
	agreementRepo repository.AgreementRepository,
	logger *zap.Logger,
) *LegacyValidationStrategy {
	return &LegacyValidationStrategy{
		agreementRepo: agreementRepo,
		logger:        logger,
	}
}

// Name returns the strategy name.
func (s *LegacyValidationStrategy) Name() string {
	return "legacy"
}

// ValidateAndPersist validates a JSON legacy agreement and saves it.
func (s *LegacyValidationStrategy) ValidateAndPersist(ctx context.Context, steward string, payload []byte) error {
	var agreement Agreement
	if err := json.Unmarshal(payload, &agreement); err != nil {
		s.logger.Error("Failed to unmarshal legacy agreement", zap.Error(err))
		return fmt.Errorf("invalid legacy agreement JSON: %w", err)
	}

	if agreement.Name != steward {
		s.logger.Error("Agreement name mismatch", zap.String("expected", steward), zap.String("actual", agreement.Name))
		return fmt.Errorf("agreement name mismatch: expected %s, got %s", steward, agreement.Name)
	}

	if err := s.agreementRepo.SaveAgreement(steward, &agreement); err != nil {
		s.logger.Error("Failed to save agreement", zap.Error(err))
		return fmt.Errorf("failed to save agreement: %w", err)
	}

	return nil
}

// Validate validates a user's access using the legacy JSON agreement approach.
func (s *LegacyValidationStrategy) Validate(steward, userName string) *ValidationResult {
	// Fetch agreement from repository
	agreement, found, err := s.agreementRepo.GetAgreement(steward)
	if err != nil {
		s.logger.Error("Failed to retrieve agreement",
			zap.String("steward", steward),
			zap.Error(err),
		)
		return invalidResult(steward, "error retrieving agreement")
	}

	if !found {
		s.logger.Info("Agreement not found for steward",
			zap.String("steward", steward),
		)
		return invalidResult(steward, "agreement not found")
	}

	// Validate user access against the agreement
	return s.validateUserAccess(agreement, userName, steward)
}

// validateUserAccess validates whether a user has access based on an agreement.
func (s *LegacyValidationStrategy) validateUserAccess(agreement *Agreement, userName, steward string) *ValidationResult {
	result := &ValidationResult{
		Steward: steward,
		IsValid: false,
	}

	// Check if user exists in agreement relations
	userRelation, exists := agreement.Relations[userName]
	if !exists {
		result.InvalidReason = "user not found in agreement relations"
		return result
	}
	result.UserRelation = &userRelation

	// Match user's allowed archetypes with agreement's supported archetypes
	matchedArchetypes, _ := lib.GetMatchedElements(userRelation.AllowedArchetypes, agreement.Archetypes)
	if len(matchedArchetypes) == 0 {
		result.InvalidReason = "no matching archetypes between user permissions and agreement"
		return result
	}
	result.MatchedArchetypes = matchedArchetypes

	// Match user's allowed compute providers with agreement's compute providers
	matchedCompute, _ := lib.GetMatchedElements(userRelation.AllowedComputeProviders, agreement.ComputeProviders)
	result.MatchedComputeProvs = matchedCompute

	result.IsValid = true
	return result
}

// Agreement is a type alias for api.Agreement to simplify imports.
type Agreement = api.Agreement
