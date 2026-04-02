// Package policyenforcer provides the policy enforcement layer that uses
// a Reasoner interface to validate requests and retrieve allowed clauses.
// This allows the policy enforcer to work with different reasoning backends.
package policyenforcer

import "github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/reasoner"

// -----------------------------------------------------------------------------
// Request Types
// -----------------------------------------------------------------------------

// AllowedClausesRequest represents a request to get allowed clauses for a requester.
type AllowedClausesRequest struct {
	Organization string `json:"organization"` // The organization/steward
	Requester    string `json:"requester"`    // The user/requester
}

// ValidateRequestParams represents a request to validate if a specific operation is allowed.
type ValidateRequestParams struct {
	Organization    string `json:"organization"`     // The data steward organization
	Requester       string `json:"requester"`        // The user making the request
	RequestType     string `json:"request_type"`     // Type of request (e.g., "sqlDataRequest")
	DataSet         string `json:"data_set"`         // The dataset being requested
	Archetype       string `json:"archetype"`        // The processing archetype
	ComputeProvider string `json:"compute_provider"` // Where the computation runs
}

// ToReasonerParams converts the request to reasoner.RequestParams.
func (r *ValidateRequestParams) ToReasonerParams() reasoner.RequestParams {
	return reasoner.RequestParams{
		Organization:    r.Organization,
		Requester:       r.Requester,
		RequestType:     r.RequestType,
		DataSet:         r.DataSet,
		Archetype:       r.Archetype,
		ComputeProvider: r.ComputeProvider,
	}
}

// -----------------------------------------------------------------------------
// Response Types
// -----------------------------------------------------------------------------

// AllAllowedClausesResponse contains all allowed clauses for a requester at an organization.
type AllAllowedClausesResponse struct {
	Organization     string   `json:"organization"`      // The organization/steward
	Requester        string   `json:"requester"`         // The user/requester
	RequestTypes     []string `json:"request_types"`     // Allowed request types
	DataSets         []string `json:"data_sets"`         // Allowed datasets
	Archetypes       []string `json:"archetypes"`        // Allowed archetypes
	ComputeProviders []string `json:"compute_providers"` // Allowed compute providers
}

// ValidationResponse represents the response from validating a request.
type ValidationResponse struct {
	Allowed         bool   `json:"allowed"`                    // Whether the request is permitted
	Reason          string `json:"reason,omitempty"`           // Explanation for the decision
	Organization    string `json:"organization"`               // The organization checked
	Requester       string `json:"requester"`                  // The requester checked
	RequestType     string `json:"request_type,omitempty"`     // The request type checked
	DataSet         string `json:"data_set,omitempty"`         // The dataset checked
	Archetype       string `json:"archetype,omitempty"`        // The archetype checked
	ComputeProvider string `json:"compute_provider,omitempty"` // The compute provider checked
	DebugResponse   string `json:"debug_response,omitempty"`   // DEBUG: Raw response from the reasoner (temporary)
}

// ErrorResponse represents an error response.
type ErrorResponse struct {
	Error string `json:"error"` // Human-readable error message
}

