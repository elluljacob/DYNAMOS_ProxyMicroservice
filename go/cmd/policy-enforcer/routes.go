package main

import (
	"net/http"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/eflint"
	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/httpapi"
	policyenforcerhttp "github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/policyenforcerhttp"
	"go.opencensus.io/plugin/ochttp"
)

// RegisterRoutes registers all HTTP routes on the provided apiMux.
// It separates concerns by grouping routes into health, eflint, pool, and policy-enforcer categories.
func RegisterRoutes(
	apiMux *http.ServeMux,
	instanceAPIHandler *eflint.InstanceAPIHandler,
	stateAPIHandler *eflint.StateAPIHandler,
	policyEnforcerHandler *policyenforcerhttp.HTTPHandler,
	pool *eflint.InstancePool,
) {
	// Health check endpoint
	apiMux.Handle("/health", http.HandlerFunc(healthHandler))

	// eFLINT instance management endpoints
	registerEflintInstanceRoutes(apiMux, instanceAPIHandler)

	// eFLINT pool management endpoints
	registerEflintPoolRoutes(apiMux, instanceAPIHandler)

	// eFLINT state management endpoints
	registerEflintStateRoutes(apiMux, stateAPIHandler)

	// Policy enforcer endpoints
	registerPolicyEnforcerRoutes(apiMux, policyEnforcerHandler)
}

// registerEflintInstanceRoutes registers routes for eFLINT instance lifecycle management.
func registerEflintInstanceRoutes(mux *http.ServeMux, h *eflint.InstanceAPIHandler) {
	mux.Handle("/eflint/status", &ochttp.Handler{
		Handler: httpapi.RequireGET(h.GetStatus),
	})
	mux.Handle("/eflint/start", &ochttp.Handler{
		Handler: httpapi.RequirePOST(h.Start),
	})
	mux.Handle("/eflint/stop", &ochttp.Handler{
		Handler: httpapi.RequirePOST(h.Stop),
	})
	mux.Handle("/eflint/restart", &ochttp.Handler{
		Handler: httpapi.RequirePOST(h.Restart),
	})
	mux.Handle("/eflint/command", &ochttp.Handler{
		Handler: httpapi.RequirePOST(h.SendCommand),
	})
}

// registerEflintPoolRoutes registers routes for eFLINT instance pool management.
func registerEflintPoolRoutes(mux *http.ServeMux, h *eflint.InstanceAPIHandler) {
	mux.Handle("/eflint/instances", &ochttp.Handler{
		Handler: httpapi.RequireGET(h.ListInstances),
	})
	mux.Handle("/eflint/instances/pool-size", &ochttp.Handler{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				h.GetPoolSize(w, r)
			case http.MethodPut:
				h.SetPoolSize(w, r)
			default:
				httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
		}),
	})
}

// registerEflintStateRoutes registers routes for eFLINT state management.
func registerEflintStateRoutes(mux *http.ServeMux, h *eflint.StateAPIHandler) {
	mux.Handle("/eflint/state", &ochttp.Handler{
		Handler: httpapi.RequireGET(h.GetState),
	})
	mux.Handle("/eflint/state/export", &ochttp.Handler{
		Handler: httpapi.RequirePOST(h.ExportState),
	})
	mux.Handle("/eflint/state/import", &ochttp.Handler{
		Handler: httpapi.RequirePOST(h.ImportState),
	})
	mux.Handle("/eflint/state/checkpoint", &ochttp.Handler{
		Handler: httpapi.RequirePOST(h.CreateCheckpoint),
	})
	mux.Handle("/eflint/state/checkpoint/restore", &ochttp.Handler{
		Handler: httpapi.RequirePOST(h.RestoreCheckpoint),
	})
	mux.Handle("/eflint/state/checkpoints", &ochttp.Handler{
		Handler: httpapi.RequireGET(h.ListCheckpoints),
	})
	mux.Handle("/eflint/state/checkpoint/", &ochttp.Handler{
		Handler: httpapi.RequireDELETE(h.DeleteCheckpoint),
	})
}

// registerPolicyEnforcerRoutes registers routes for the policy enforcer API.
func registerPolicyEnforcerRoutes(mux *http.ServeMux, h *policyenforcerhttp.HTTPHandler) {
	// Allowed clauses endpoint (GET) - returns all clause types in a single call
	mux.Handle("/policy-enforcer/allowed-clauses", &ochttp.Handler{
		Handler: httpapi.RequireGET(h.GetAllAllowedClauses),
	})

	// Validation endpoint (POST)
	mux.Handle("/policy-enforcer/validate", &ochttp.Handler{
		Handler: httpapi.RequirePOST(h.ValidateRequest),
	})
}

// healthHandler returns a simple health check response.
func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
}
