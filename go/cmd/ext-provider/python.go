package main

import (
	"context"
	"fmt"
	"strings"
)

func ExecutePython(ctx context.Context, role, pythonCode string) (string, error) {
	if pythonCode == "" {
		return "", fmt.Errorf("no python code provided")
	}

	rewrittenCode := rewritePythonTableRefs(role, pythonCode)
	procName := fmt.Sprintf("DYNAMOS_PROC_%s", strings.ToUpper(role))

	// Extract just the function body if user submitted a full def run(...): block
	body := extractFunctionBody(rewrittenCode)

	createProc := fmt.Sprintf(`CREATE OR REPLACE PROCEDURE %s()
RETURNS STRING
LANGUAGE PYTHON
RUNTIME_VERSION = '3.11'
PACKAGES = ('snowflake-snowpark-python', 'pandas')
HANDLER = 'run'
AS
$$
import snowflake.snowpark as snowpark
import json

def run(session: snowpark.Session) -> str:
%s
$$`, procName, indentCode(body))

	logger.Sugar().Infof("Creating Python procedure: %s", procName)
	logger.Sugar().Debugf("Procedure DDL:\n%s", createProc)

	_, _, err := QuerySnowflake(ctx, createProc)
	if err != nil {
		return "", fmt.Errorf("failed to create python procedure: %w", err)
	}

	callSQL := fmt.Sprintf("CALL %s()", procName)
	rows, _, err := QuerySnowflake(ctx, callSQL)
	if err != nil {
		return "", fmt.Errorf("failed to execute python procedure: %w", err)
	}

	if len(rows) == 0 || len(rows[0]) == 0 {
		return "0", nil
	}

	// The procedure returns a single string value — return it directly
	return strings.TrimSpace(rows[0][0]), nil
}

// extractFunctionBody strips the "def run(...):" wrapper if present,
// returning just the indented body lines
func extractFunctionBody(code string) string {
	lines := strings.Split(strings.TrimSpace(code), "\n")

	// Find the def run line and extract everything after it
	bodyStart := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "def run") {
			bodyStart = i + 1
			break
		}
	}

	if bodyStart == -1 || bodyStart >= len(lines) {
		// No def run found, assume it's already just the body
		return code
	}

	// Extract body lines and strip one level of indentation
	bodyLines := lines[bodyStart:]
	dedented := make([]string, 0, len(bodyLines))
	for _, line := range bodyLines {
		if strings.HasPrefix(line, "    ") {
			dedented = append(dedented, line[4:]) // strip 4 spaces
		} else if strings.HasPrefix(line, "\t") {
			dedented = append(dedented, line[1:]) // strip one tab
		} else {
			dedented = append(dedented, line)
		}
	}

	return strings.Join(dedented, "\n")
}

func indentCode(code string) string {
	lines := strings.Split(strings.TrimSpace(code), "\n")
	indented := make([]string, len(lines))
	for i, line := range lines {
		if line == "" {
			indented[i] = ""
		} else {
			indented[i] = "    " + line
		}
	}
	return strings.Join(indented, "\n")
}

// rewritePythonTableRefs replaces table names in Python code with role-appropriate views
func rewritePythonTableRefs(role, code string) string {
	globalPolicyMu.RLock()
	policy := globalPolicy
	globalPolicyMu.RUnlock()

	if policy == nil {
		return code
	}

	// Find the permission for this role
	var perm *Permission
	for i, p := range policy.Permissions {
		if strings.EqualFold(p.Assignee, role) {
			perm = &policy.Permissions[i]
			break
		}
	}
	if perm == nil {
		return code
	}

	rewritten := code
	for tableName := range perm.Tables {
		view := viewNameForRole(role, tableName)
		rewritten = replaceTableName(rewritten, tableName, view)
	}

	return rewritten
}
