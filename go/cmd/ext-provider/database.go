package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
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
// QuerySnowflake returns structured rows and column names
func QuerySnowflake(ctx context.Context, query string) ([][]string, []string, error) {
	if snowflakeDB == nil {
		if err := InitDB(); err != nil {
			return nil, nil, err
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	logger.Debug("Attempting to query Snowflake...")

	rows, err := snowflakeDB.QueryContext(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}

	count := len(columns)
	values := make([]interface{}, count)
	valuePtrs := make([]interface{}, count)
	for i := range columns {
		valuePtrs[i] = &values[i]
	}

	var result [][]string
	for rows.Next() {
		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, nil, fmt.Errorf("row scan failed: %w", err)
		}
		row := make([]string, count)
		for i, val := range values {
			switch v := val.(type) {
			case []byte:
				row[i] = string(v)
			case nil:
				row[i] = ""
			default:
				row[i] = fmt.Sprintf("%v", v)
			}
		}
		result = append(result, row)
	}

	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("error during row iteration: %w", err)
	}

	return result, columns, nil
}
