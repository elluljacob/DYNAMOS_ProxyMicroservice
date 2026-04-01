package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/snowflakedb/gosnowflake"
)

// Global DB instance to prevent re-authenticating on every request
var (
	snowflakeDB *sql.DB
	dbOnce      sync.Once
)

func InitDB() error {
	var err error
	dsn := os.Getenv("SNOWFLAKE_DSN")

	// Open doesn't connect, it just validates arguments
	snowflakeDB, err = sql.Open("snowflake", dsn)
	if err != nil {
		return fmt.Errorf("failed to open snowflake driver: %w", err)
	}

	// configure connection pool settings if needed
	snowflakeDB.SetMaxOpenConns(10)
	snowflakeDB.SetMaxIdleConns(5)
	snowflakeDB.SetConnMaxLifetime(1 * time.Hour)

	// Verify connection immediately
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := snowflakeDB.PingContext(ctx); err != nil {
		return fmt.Errorf("failed to ping snowflake: %w", err)
	}

	return nil
}

// QuerySnowflake handles the execution using the existing pool
func QuerySnowflake(ctx context.Context, query string) (string, error) {
	if snowflakeDB == nil {
		// Fallback or lazy init if InitDB wasn't called (safety net)
		if err := InitDB(); err != nil {
			return "Error Init DB", err
		}
	}

	// Use a timeout for the Query
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	logger.Debug("Attempting to query Snowflake...")

	// QueryContext
	rows, err := snowflakeDB.QueryContext(ctx, query)
	if err != nil {
		return "Error executing query", err // Return raw error to handler for inspection
	}
	defer rows.Close()

	// Scan Results
	var result strings.Builder
	var hasResults bool

	// Get column names to handle dynamic results better
	columns, _ := rows.Columns()
	count := len(columns)
	values := make([]interface{}, count)
	valuePtrs := make([]interface{}, count)

	for rows.Next() {
		hasResults = true
		// Initialize pointers
		for i := range columns {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return "Error scanning row", fmt.Errorf("row scan failed: %w", err)
		}

		// Simple formatter
		for i, val := range values {
			if i > 0 {
				result.WriteString(", ")
			}
			// Handle nil/bytes/etc
			switch v := val.(type) {
			case []byte:
				result.Write(v)
			default:
				result.WriteString(fmt.Sprintf("%v", v))
			}
		}
	}

	// Check for errors that occurred *during* iteration
	if err := rows.Err(); err != nil {
		return "Error during row iteration", fmt.Errorf("error during row iteration: %w", err)
	}

	if !hasResults {
		return "0", nil
	}

	return result.String(), nil
}
