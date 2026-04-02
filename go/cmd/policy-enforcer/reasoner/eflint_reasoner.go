package reasoner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/eflint"
	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/repository"
)

// -----------------------------------------------------------------------------
// eFLINT Reasoner Implementation
// -----------------------------------------------------------------------------

// EflintReasoner implements the Reasoner interface using an eFLINT server pool.
// For each request it acquires an idle instance from the pool, loads the
// organization's eFLINT model, executes the query, and releases the instance.
type EflintReasoner struct {
	pool      *eflint.InstancePool
	modelRepo repository.EflintModelRepository
	logger    *zap.Logger
}

// NewEflintReasoner creates a new eFLINT-based reasoner backed by an instance pool.
func NewEflintReasoner(pool *eflint.InstancePool, modelRepo repository.EflintModelRepository, logger *zap.Logger) *EflintReasoner {
	return &EflintReasoner{
		pool:      pool,
		modelRepo: modelRepo,
		logger:    logger,
	}
}

// Name returns the name of this reasoner.
func (r *EflintReasoner) Name() string {
	return "eflint"
}

// IsRunning checks if the eFLINT instance pool is operational.
func (r *EflintReasoner) IsRunning() bool {
	return r.pool.GetTargetSize() > 0
}

// ValidateAndPersistModel validates a raw eFLINT model by sending and persisting it.
func (r *EflintReasoner) ValidateAndPersistModel(ctx context.Context, organization string, modelText string) error {
	entry, err := r.pool.Acquire()
	if err != nil {
		return fmt.Errorf("failed to acquire eFLINT instance: %w", err)
	}
	defer r.pool.Release(entry)

	if _, err := entry.Manager.SendPhrases(modelText); err != nil {
		r.logger.Error("invalid eFLINT specification", zap.Error(err))
		return fmt.Errorf("invalid eFLINT specification: %w", err)
	}

	if err := r.modelRepo.SaveEflintModel(organization, modelText); err != nil {
		r.logger.Error("failed to save eFLINT model", zap.Error(err))
		return fmt.Errorf("failed to save eFLINT model: %w", err)
	}

	return nil
}

// -----------------------------------------------------------------------------
// Pool Lifecycle Helper
// -----------------------------------------------------------------------------

// withLoadedInstance acquires an instance from the pool, loads the eFLINT model
// for the given organization, and calls fn with the entry's Manager. The instance
// is released asynchronously after fn completes.
func (r *EflintReasoner) withLoadedInstance(ctx context.Context, organization string, fn func(mgr *eflint.Manager) error) error {
	entry, err := r.pool.Acquire()
	if err != nil {
		return fmt.Errorf("failed to acquire eFLINT instance: %w", err)
	}

	modelText, found, err := r.modelRepo.GetEflintModel(organization)
	if err != nil {
		r.logger.Error("failed to retrieve eFLINT model",
			zap.String("organization", organization),
			zap.Error(err),
		)
		go r.pool.Release(entry)
		return fmt.Errorf("failed to retrieve eFLINT model for %s: %w", organization, err)
	}
	if !found {
		r.logger.Error("eFLINT model not found for organization",
			zap.String("organization", organization),
		)
		go r.pool.Release(entry)
		return fmt.Errorf("eFLINT model not found for organization %s", organization)
	}

	if _, err := entry.Manager.SendPhrases(modelText); err != nil {
		r.logger.Error("failed to load eFLINT specification",
			zap.String("organization", organization),
			zap.Error(err),
		)
		go r.pool.Release(entry)
		return fmt.Errorf("failed to load eFLINT specification: %w", err)
	}

	err = fn(entry.Manager)
	go r.pool.Release(entry)
	return err
}

// -----------------------------------------------------------------------------
// Allowed Clauses Retrieval
// -----------------------------------------------------------------------------

// GetAllowedRequestTypes returns all request types allowed for a requester at an organization.
func (r *EflintReasoner) GetAllowedRequestTypes(ctx context.Context, organization, requester string) ([]string, error) {
	var result []string

	err := r.withLoadedInstance(ctx, organization, func(mgr *eflint.Manager) error {
		facts, err := fetchFacts(mgr)
		if err != nil {
			return err
		}
		result = filterAllowedClauses(facts, "allowed-request-type", "request-type", organization, requester)
		return nil
	})

	return result, err
}

// GetAllowedDataSets returns all datasets allowed for a requester at an organization.
func (r *EflintReasoner) GetAllowedDataSets(ctx context.Context, organization, requester string) ([]string, error) {
	var result []string

	err := r.withLoadedInstance(ctx, organization, func(mgr *eflint.Manager) error {
		facts, err := fetchFacts(mgr)
		if err != nil {
			return err
		}
		result = filterAllowedClauses(facts, "allowed-data-set", "data-set", organization, requester)
		return nil
	})

	return result, err
}

// GetAllowedArchetypes returns all archetypes allowed for a requester at an organization.
func (r *EflintReasoner) GetAllowedArchetypes(ctx context.Context, organization, requester string) ([]string, error) {
	var result []string

	err := r.withLoadedInstance(ctx, organization, func(mgr *eflint.Manager) error {
		facts, err := fetchFacts(mgr)
		if err != nil {
			return err
		}
		result = filterAllowedClauses(facts, "allowed-archetype", "archetype", organization, requester)
		return nil
	})

	return result, err
}

// GetAllowedComputeProviders returns all compute providers allowed for a requester at an organization.
func (r *EflintReasoner) GetAllowedComputeProviders(ctx context.Context, organization, requester string) ([]string, error) {
	var result []string

	err := r.withLoadedInstance(ctx, organization, func(mgr *eflint.Manager) error {
		facts, err := fetchFacts(mgr)
		if err != nil {
			return err
		}
		result = filterAllowedClauses(facts, "allowed-compute-provider", "compute-provider", organization, requester)
		return nil
	})

	return result, err
}

// GetAllAllowedClauses returns all allowed clauses for a requester at an organization.
// This is more efficient than calling the individual methods because it only acquires
// one instance and fetches facts once.
func (r *EflintReasoner) GetAllAllowedClauses(ctx context.Context, organization, requester string) (*AllAllowedClauses, error) {
	var result *AllAllowedClauses

	err := r.withLoadedInstance(ctx, organization, func(mgr *eflint.Manager) error {
		facts, err := fetchFacts(mgr)
		if err != nil {
			return err
		}

		result = &AllAllowedClauses{
			RequestTypes:     filterAllowedClauses(facts, "allowed-request-type", "request-type", organization, requester),
			DataSets:         filterAllowedClauses(facts, "allowed-data-set", "data-set", organization, requester),
			Archetypes:       filterAllowedClauses(facts, "allowed-archetype", "archetype", organization, requester),
			ComputeProviders: filterAllowedClauses(facts, "allowed-compute-provider", "compute-provider", organization, requester),
		}
		return nil
	})

	return result, err
}

// -----------------------------------------------------------------------------
// Request Validation
// -----------------------------------------------------------------------------

// IsRequestAllowed checks if a specific request is permitted according to the eFLINT policy.
// It uses the "enabled" command on the submit-request act to determine if the request is allowed.
func (r *EflintReasoner) IsRequestAllowed(ctx context.Context, params RequestParams) (*RequestValidationResult, error) {
	var result *RequestValidationResult

	err := r.withLoadedInstance(ctx, params.Organization, func(mgr *eflint.Manager) error {
		// Build the eFLINT "enabled" command with a properly structured VALUE
		// This checks if the submit-request action is enabled with the given parameters
		cmd := map[string]interface{}{
			"command": "enabled",
			"value": map[string]interface{}{
				"fact-type": "submit-request",
				"value": []map[string]interface{}{
					{"fact-type": "req", "value": params.Requester},
					{"fact-type": "org", "value": params.Organization},
					{"fact-type": "rtype", "value": params.RequestType},
					{"fact-type": "dataset", "value": params.DataSet},
					{"fact-type": "arch", "value": params.Archetype},
					{"fact-type": "provider", "value": params.ComputeProvider},
				},
			},
		}

		cmdJSON, err := json.Marshal(cmd)
		if err != nil {
			return fmt.Errorf("failed to marshal command: %w", err)
		}

		response, err := mgr.SendCommand(string(cmdJSON))
		if err != nil {
			return fmt.Errorf("failed to query eFLINT: %w", err)
		}

		r.logger.Debug("eFLINT enabled query response", zap.String("command", string(cmdJSON)))

		result, err = parseValidationResponse(response, params)
		return err
	})

	return result, err
}

// -----------------------------------------------------------------------------
// Availability Provider Implementation
// -----------------------------------------------------------------------------

// GetAvailableArchetypes returns archetypes available at an organization.
func (r *EflintReasoner) GetAvailableArchetypes(ctx context.Context, organization string) ([]string, error) {
	var result []string

	err := r.withLoadedInstance(ctx, organization, func(mgr *eflint.Manager) error {
		facts, err := fetchFacts(mgr)
		if err != nil {
			return err
		}
		result = filterAvailableFacts(facts, "available-archetype", "archetype", organization)
		return nil
	})

	return result, err
}

// GetAvailableComputeProviders returns compute providers available at an organization.
func (r *EflintReasoner) GetAvailableComputeProviders(ctx context.Context, organization string) ([]string, error) {
	var result []string

	err := r.withLoadedInstance(ctx, organization, func(mgr *eflint.Manager) error {
		facts, err := fetchFacts(mgr)
		if err != nil {
			return err
		}
		result = filterAvailableFacts(facts, "available-compute-provider", "compute-provider", organization)
		return nil
	})

	return result, err
}

// -----------------------------------------------------------------------------
// Helper Types and Functions
// -----------------------------------------------------------------------------

// eflintFact represents a fact from the eFLINT facts response.
type eflintFact struct {
	FactType   string `json:"fact-type"`
	TaggedType string `json:"tagged-type"`
	Arguments  []struct {
		FactType string `json:"fact-type"`
		Value    string `json:"value"`
	} `json:"arguments"`
}

// fetchFacts sends the "facts" command to the given manager and parses the response.
func fetchFacts(mgr *eflint.Manager) ([]eflintFact, error) {
	response, err := mgr.SendCommand(`{"command": "facts"}`)
	if err != nil {
		return nil, fmt.Errorf("failed to get facts from eFLINT: %w", err)
	}

	facts, err := parseFactsResponse(response)
	if err != nil {
		return nil, fmt.Errorf("failed to parse facts response: %w", err)
	}

	return facts, nil
}

// parseFactsResponse parses the JSON response from an eFLINT "facts" command.
func parseFactsResponse(response string) ([]eflintFact, error) {
	var factsResponse struct {
		Values []eflintFact `json:"values"`
	}

	if err := json.Unmarshal([]byte(response), &factsResponse); err != nil {
		return nil, err
	}

	return factsResponse.Values, nil
}

// filterAllowedClauses filters pre-fetched facts for allowed clauses.
// This is a pure function that doesn't make any network calls.
func filterAllowedClauses(
	facts []eflintFact,
	factType string, // e.g., "allowed-archetype"
	valueFactType string, // e.g., "archetype"
	organization string,
	requester string,
) []string {
	var values []string
	for _, fact := range facts {
		if fact.FactType == factType && len(fact.Arguments) >= 3 {
			// Arguments: [0]=organization, [1]=requester, [2]=value
			if fact.Arguments[0].FactType == "organization" &&
				fact.Arguments[0].Value == organization &&
				fact.Arguments[1].FactType == "requester" &&
				fact.Arguments[1].Value == requester &&
				fact.Arguments[2].FactType == valueFactType {
				values = append(values, fact.Arguments[2].Value)
			}
		}
	}
	return values
}

// filterAvailableFacts filters pre-fetched facts for available resources at an organization.
// This is a pure function that doesn't make any network calls.
func filterAvailableFacts(
	facts []eflintFact,
	factType string,
	valueFactType string,
	organization string,
) []string {
	var values []string
	for _, fact := range facts {
		if fact.FactType == factType && len(fact.Arguments) >= 2 {
			// Arguments: [0]=organization, [1]=value
			if fact.Arguments[0].FactType == "organization" &&
				fact.Arguments[0].Value == organization &&
				fact.Arguments[1].FactType == valueFactType {
				values = append(values, fact.Arguments[1].Value)
			}
		}
	}
	return values
}

// parseValidationResponse parses the eFLINT response for an "enabled" query.
// The enabled command returns a Status response with query-results containing "success" if enabled.
func parseValidationResponse(response string, params RequestParams) (*RequestValidationResult, error) {
	var resp struct {
		Response     string   `json:"response"`
		QueryResults []string `json:"query-results"` // eFLINT returns "success" when enabled
		Errors       []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"errors"`
		Violations []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"violations"`
	}

	if err := json.Unmarshal([]byte(response), &resp); err != nil {
		return nil, fmt.Errorf("failed to parse eFLINT response: %w", err)
	}

	// Check if the enabled query succeeded
	// The query-results array contains "success" when the action is enabled
	isEnabled := len(resp.QueryResults) > 0 && strings.EqualFold(resp.QueryResults[0], "success")

	result := &RequestValidationResult{
		Allowed: isEnabled && len(resp.Violations) == 0 && len(resp.Errors) == 0,
	}

	// Build reason from errors or violations
	var reasons []string
	for _, err := range resp.Errors {
		reasons = append(reasons, err.Message)
	}
	for _, v := range resp.Violations {
		reasons = append(reasons, v.Message)
	}

	if len(reasons) > 0 {
		result.Reason = strings.Join(reasons, "; ")
	} else if result.Allowed {
		result.Reason = "Request is permitted by the agreement"
	} else {
		result.Reason = "Request is not permitted by the agreement"
	}

	return result, nil
}

// Ensure EflintReasoner implements the interfaces
var _ Reasoner = (*EflintReasoner)(nil)
var _ AvailabilityProvider = (*EflintReasoner)(nil)
