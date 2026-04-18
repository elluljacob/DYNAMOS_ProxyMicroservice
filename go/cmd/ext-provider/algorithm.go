package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type RequestBody struct {
	Algorithm string `json:"algorithm"`
	Query     string `json:"query"`
	Type      string `json:"type"`
	Options   struct {
		Graph     bool `json:"graph"`
		Aggregate bool `json:"aggregate"`
	} `json:"options"`
	User struct {
		ID       string `json:"id"`
		UserName string `json:"user_name"`
	} `json:"user"`
	PythonCode string `json:"python_code"` // for pythonDataRequest
}

// ProcessResult applies the algorithm to the raw query results
func ProcessResult(algorithm string, rows [][]string, columns []string) (string, error) {
	switch strings.ToLower(algorithm) {
	case "average":
		return computeAverage(rows, columns)
	case "aggregate", "":
		return aggregateRaw(rows, columns)
	default:
		logger.Sugar().Warnf("Unknown algorithm %s, defaulting to aggregate", algorithm)
		return aggregateRaw(rows, columns)
	}
}

// computeAverage mimics the sql-algorithm average microservice
// computes avg salary by gender (GESLACHT + SALSCHAL)
func computeAverage(rows [][]string, columns []string) (string, error) {
	geslachtIdx := -1
	salschalIdx := -1

	for i, col := range columns {
		upper := strings.ToUpper(col)
		if upper == "GESLACHT" {
			geslachtIdx = i
		}
		if upper == "SALSCHAL" {
			salschalIdx = i
		}
	}

	if geslachtIdx == -1 || salschalIdx == -1 {
		return "", fmt.Errorf("GESLACHT or SALSCHAL column not found in result set")
	}

	var totalMale, totalFemale float64
	maleCount, femaleCount := 0, 0

	for _, row := range rows {
		if len(row) <= geslachtIdx || len(row) <= salschalIdx {
			continue
		}
		gender := strings.TrimSpace(row[geslachtIdx])
		salaryStr := strings.TrimSpace(row[salschalIdx])

		if salaryStr == "" || salaryStr == "0" {
			continue
		}

		salary, err := strconv.ParseFloat(salaryStr, 64)
		if err != nil {
			continue
		}

		switch gender {
		case "M":
			totalMale += salary
			maleCount++
		case "V":
			totalFemale += salary
			femaleCount++
		}
	}

	result := make(map[string]string)
	if maleCount > 0 {
		result["avg_salary_scale_men"] = fmt.Sprintf("%.3f", totalMale/float64(maleCount))
	}
	if femaleCount > 0 {
		result["avg_salary_scale_women"] = fmt.Sprintf("%.3f", totalFemale/float64(femaleCount))
	}

	return marshalResult(result)
}

// aggregateRaw returns the raw rows as-is
func aggregateRaw(rows [][]string, columns []string) (string, error) {
	result := make([][]string, 0, len(rows)+1)
	result = append(result, columns)
	result = append(result, rows...)
	return marshalResult(result)
}

func marshalResult(v interface{}) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("failed to marshal result: %w", err)
	}
	return string(b), nil
}
