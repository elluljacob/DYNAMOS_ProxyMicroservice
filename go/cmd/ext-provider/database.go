package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/snowflakedb/gosnowflake"
)

// QuerySnowflake handles the connection lifecycle and execution
func QuerySnowflake(ctx context.Context, query string) (string, error) {
	// Get DSN (Environment Variable)
	dsn := os.Getenv("SNOWFLAKE_DSN")

	// Open Connection
	db, err := sql.Open("snowflake", dsn)
	if err != nil {
		return "", fmt.Errorf("failed to open snowflake connection: %w", err)
	}
	defer db.Close()

	// 5-second timeout on the DB execution (rn shouldn't take long so this is fine but change later )
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Execute
	logger.Debug("Attempting to query Snowflake...")
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return "", fmt.Errorf("query execution failed: %w", err)
	}
	defer rows.Close()

	// Scan Results
	var result strings.Builder
	for rows.Next() {
		var val interface{}
		if err := rows.Scan(&val); err != nil {
			continue
		}
		result.WriteString(fmt.Sprintf("%v", val))
	}

	finalResult := result.String()
	if finalResult == "" {
		return "0", nil
	}

	return finalResult, nil
}
