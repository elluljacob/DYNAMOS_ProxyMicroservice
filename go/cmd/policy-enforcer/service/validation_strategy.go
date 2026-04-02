package service

import (
	"context"

	"github.com/Jorrit05/DYNAMOS/pkg/api"
)

// ValidationStrategy defines the interface for different validation approaches.
// This follows the Strategy pattern, allowing new validation methods to be added
// without modifying existing code (Open/Closed Principle).
type ValidationStrategy interface {
	// Validate validates a user's access to a data provider.
	Validate(steward, userName string) *ValidationResult

	// ValidateAndPersist validates a new policy payload and persists it if valid.
	ValidateAndPersist(ctx context.Context, steward string, payload []byte) error

	// Name returns the strategy name for logging/debugging.
	Name() string
}

// ValidationResult represents the result of validating a user against a data provider's agreement.
type ValidationResult struct {
	Steward             string
	IsValid             bool
	InvalidReason       string
	MatchedArchetypes   []string
	MatchedComputeProvs []string
	UserRelation        *api.Relation
}

// invalidResult creates an invalid ValidationResult with the given reason.
func invalidResult(steward, reason string) *ValidationResult {
	return &ValidationResult{
		Steward:       steward,
		IsValid:       false,
		InvalidReason: reason,
	}
}
