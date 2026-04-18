package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type DataRequest struct {
	Query string `json:"query"`
}

// main HTTP handler for the agent
func HandleSQLRequest() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Sugar().Infof("[HTTP] %s request on %s", r.Method, r.URL.Path)

		bodyBytes, _ := io.ReadAll(r.Body)
		logger.Sugar().Debugf("Request body: %s", string(bodyBytes))

		var req RequestBody
		json.Unmarshal(bodyBytes, &req)
		logger.Sugar().Debugf("Parsed userName: %q", req.User.UserName)

		role := getRoleForUser(req.User.UserName)
		logger.Sugar().Infof("User %q assigned role: %s", req.User.UserName, role)

		var result string
		var err error

		switch strings.ToLower(req.Type) {
		case "pythondatarequest":
			result, err = ExecutePython(r.Context(), role, req.PythonCode)
			if err != nil {
				handleDBError(w, err)
				return
			}

		default: // sqlDataRequest
			query := req.Query
			if query == "" {
				query = string(bodyBytes)
			}

			rewritten, err := RewriteQuery(role, query)
			if err != nil {
				http.Error(w, fmt.Sprintf("Policy error: %v", err), http.StatusForbidden)
				return
			}

			rows, cols, err := QuerySnowflake(r.Context(), rewritten)
			if err != nil {
				handleDBError(w, err)
				return
			}

			result, err = ProcessResult(req.Algorithm, rows, cols)
			if err != nil {
				http.Error(w, fmt.Sprintf("Algorithm error: %v", err), http.StatusInternalServerError)
				return
			}
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(result))
	}
}

// maps Go errors to HTTP status codes
func handleDBError(w http.ResponseWriter, err error) {
	// 1. Log the full technical error for you (the admin)
	logger.Sugar().Errorf("DB Execution Error: %v", err)

	var statusCode int

	// Default to the actual error message so the user knows what went wrong
	clientMsg := err.Error()
	lowerErr := strings.ToLower(clientMsg)

	switch {
	// Timeouts (Context Deadline)
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(lowerErr, "context deadline"):
		statusCode = http.StatusGatewayTimeout // 504
		clientMsg = "Query timed out. The database took too long to respond."

	// Client Cancelled
	case errors.Is(err, context.Canceled):
		statusCode = 499 // Client Closed Request
		clientMsg = "Request cancelled by client."

	// SQL Syntax / Logic Errors
	case strings.Contains(lowerErr, "syntax error") ||
		strings.Contains(lowerErr, "compilation error") ||
		strings.Contains(lowerErr, "does not exist") ||
		strings.Contains(lowerErr, "query execution failed") ||
		strings.Contains(lowerErr, "invalid identifier"):

		statusCode = http.StatusBadRequest // 400

	// Connection issues
	case strings.Contains(lowerErr, "connection failed") || strings.Contains(lowerErr, "failed to open"):
		statusCode = http.StatusServiceUnavailable // 503
		clientMsg = "Database service unavailable. Please try again later."

	// Default Generic Error
	default:
		statusCode = http.StatusInternalServerError // 500
		clientMsg = fmt.Sprintf("Internal Database Error: %v", err)
	}

	// Set Content-Type so clients know it's text
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(statusCode)
	w.Write([]byte(clientMsg))
}

// parseBody helper... (remains the same)
func parseBody(r *http.Request) (string, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", err
	}
	defer r.Body.Close()

	var req DataRequest
	// Try JSON, fallback to raw string
	if err := json.Unmarshal(body, &req); err != nil || req.Query == "" {
		return string(body), nil
	}

	return req.Query, nil
}
