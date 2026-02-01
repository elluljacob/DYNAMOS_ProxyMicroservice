package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type DataRequest struct {
	Query string `json:"query"`
}

// HandleSQLRequest is the main HTTP handler for the agent
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

		logger.Sugar().Infof("[HTTP] Extracted SQL Query: %s", query)

		// Call Database Logic (from database.go)
		result, err := QuerySnowflake(r.Context(), query)
		if err != nil {
			logger.Sugar().Errorf("DB Error: %v", err)
			// Return 500 but write the error so Gateway sees it
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(fmt.Sprintf("Query Error: %v", err)))
			return
		}

		// Write Response
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(result))
	}
}

// Helper to clean up parsing logic
func parseBody(r *http.Request) (string, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", err
	}

	var req DataRequest
	// Try JSON, fallback to raw string
	if err := json.Unmarshal(body, &req); err != nil || req.Query == "" {
		req.Query = string(body)
	}

	// Safety default
	if len(req.Query) < 5 {
		req.Query = "SELECT COUNT(*) FROM EMPLOYEES"
	}
	return req.Query, nil
}
