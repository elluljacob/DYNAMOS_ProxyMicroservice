package main

import (
	"fmt"
	"os"
	"strings"
)

func LoadAndLogPolicy() error {
	path := os.Getenv("POLICY_PATH")
	if path == "" {
		path = "/config/snowflake_policy/agreements.json"
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read policy file at %s: %w", path, err)
	}

	// Print first 10 lines
	lines := strings.Split(string(data), "\n")
	logger.Sugar().Infof("Policy file loaded from: %s", path)
	logger.Sugar().Infof("--- First 10 lines ---")
	for i, line := range lines {
		if i >= 10 {
			break
		}
		logger.Sugar().Infof("line %d: %s", i+1, line)
	}

	return nil
}
