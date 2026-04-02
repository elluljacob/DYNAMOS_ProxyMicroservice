// Package reasoner provides an abstraction layer for policy reasoning engines.
// This allows the policy enforcer to work with different reasoning backends
// such as eFLINT, Symboleo, or JSON-based agreement formats.
package reasoner

import "context"

// -----------------------------------------------------------------------------
// Core Types
// -----------------------------------------------------------------------------

// AllowedClause represents a permitted clause (request type, data set, archetype, or compute provider)
// that has been granted to a requester by an organization.
type AllowedClause struct {
	Organization string `json:"organization"` // The organization/steward granting the permission
	Requester    string `json:"requester"`    // The user/requester receiving the permission
	Value        string `json:"value"`        // The specific value (e.g., archetype name, dataset name)
}

// AllAllowedClauses contains all allowed clauses for a requester at an organization.
// This is returned by the optimized GetAllAllowedClauses method.
type AllAllowedClauses struct {
	RequestTypes     []string `json:"request_types"`     // Allowed request types
	DataSets         []string `json:"data_sets"`         // Allowed datasets
	Archetypes       []string `json:"archetypes"`        // Allowed archetypes
	ComputeProviders []string `json:"compute_providers"` // Allowed compute providers
}

// RequestParams contains all parameters needed to validate a data request.
type RequestParams struct {
	Organization    string `json:"organization"`     // The data steward organization
	Requester       string `json:"requester"`        // The user making the request
	RequestType     string `json:"request_type"`     // Type of request (e.g., "sqlDataRequest")
	DataSet         string `json:"data_set"`         // The dataset being requested
	Archetype       string `json:"archetype"`        // The processing archetype (e.g., "computeToData")
	ComputeProvider string `json:"compute_provider"` // Where the computation runs (e.g., "SURF")
}

// RequestValidationResult contains the outcome of a request validation.
type RequestValidationResult struct {
	Allowed     bool   `json:"allowed"`                // Whether the request is permitted
	Reason      string `json:"reason,omitempty"`       // Explanation for the decision
	RawResponse string `json:"raw_response,omitempty"` // DEBUG: Raw response from the reasoner
}

// -----------------------------------------------------------------------------
// Reasoner Interface
// -----------------------------------------------------------------------------

// Reasoner defines the interface for policy reasoning engines.
// Different implementations (eFLINT, Symboleo, JSON-based) can be used
// interchangeably by the policy enforcer.
type Reasoner interface {
	// GetAllowedRequestTypes returns all request types allowed for a requester at an organization.
	GetAllowedRequestTypes(ctx context.Context, organization, requester string) ([]string, error)

	// GetAllowedDataSets returns all datasets allowed for a requester at an organization.
	GetAllowedDataSets(ctx context.Context, organization, requester string) ([]string, error)

	// GetAllowedArchetypes returns all archetypes allowed for a requester at an organization.
	GetAllowedArchetypes(ctx context.Context, organization, requester string) ([]string, error)

	// GetAllowedComputeProviders returns all compute providers allowed for a requester at an organization.
	GetAllowedComputeProviders(ctx context.Context, organization, requester string) ([]string, error)

	// GetAllAllowedClauses returns all allowed clauses in a single call.
	// This is more efficient than calling individual methods when you need all clause types,
	// as it fetches facts from the reasoner only once.
	GetAllAllowedClauses(ctx context.Context, organization, requester string) (*AllAllowedClauses, error)

	// IsRequestAllowed checks if a specific request is permitted according to the policy.
	// This validates all aspects: request type, dataset, archetype, and compute provider.
	IsRequestAllowed(ctx context.Context, params RequestParams) (*RequestValidationResult, error)

	// IsRunning returns whether the reasoner is ready to process requests.
	IsRunning() bool

	// ValidateAndPersistModel validates and persists a new reasoning model.
	ValidateAndPersistModel(ctx context.Context, organization string, modelText string) error

	// Name returns the name/type of this reasoner (e.g., "eflint", "symboleo", "json").
	Name() string
}

// -----------------------------------------------------------------------------
// Optional Extended Interfaces
// -----------------------------------------------------------------------------

// AvailabilityProvider is an optional interface for reasoners that track
// what resources are available at an organization level (not requester-specific).
type AvailabilityProvider interface {
	// GetAvailableArchetypes returns archetypes available at an organization.
	GetAvailableArchetypes(ctx context.Context, organization string) ([]string, error)

	// GetAvailableComputeProviders returns compute providers available at an organization.
	GetAvailableComputeProviders(ctx context.Context, organization string) ([]string, error)
}

// StateManager is an optional interface for reasoners that support state management.
type StateManager interface {
	// ExportState exports the current state of the reasoner.
	ExportState(ctx context.Context) ([]byte, error)

	// ImportState imports a previously exported state.
	ImportState(ctx context.Context, state []byte) error
}
