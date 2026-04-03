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

		// Parse Body
		query, err := parseBody(r)
		if err != nil {
			logger.Sugar().Errorf("Failed to parse body: %v", err)
			http.Error(w, "Invalid Request Body", http.StatusBadRequest)
			return
		}

		logger.Sugar().Infof("[HTTP] Executing SQL Query: %s", query)

		// Call Database Logic
		result, err := QuerySnowflake(r.Context(), query)
		if err != nil {
			// Found an error? Handle it and return immediately.
			handleDBError(w, err)
			return
		}

		// Success: Write result
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
