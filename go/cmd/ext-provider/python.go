package main

import (
	"context"
	"fmt"
	"strings"
)

// ExecutePython rewrites Python code to use role-appropriate views, wraps it in
// a Snowflake stored procedure, executes it, and returns the result string.
//
// The procedure is created (or replaced) on every call, so it always reflects
// the current policy and submitted code.
func ExecutePython(ctx context.Context, role, pythonCode, userName string) (string, error) {
	if pythonCode == "" {
		return "", fmt.Errorf("no python code provided")
	}

	rewrittenCode := rewritePythonTableRefs(role, pythonCode, userName)
	procName := fmt.Sprintf("DYNAMOS_PROC_%s", strings.ToUpper(role))

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

	return strings.TrimSpace(rows[0][0]), nil
}

// extractFunctionBody strips a "def run(...):" wrapper when present, returning
// only the body lines with one level of indentation removed.  If no such
// wrapper is found the code is returned as-is, on the assumption that it is
// already a bare function body ready for embedding in the stored procedure.
func extractFunctionBody(code string) string {
	lines := strings.Split(strings.TrimSpace(code), "\n")

	bodyStart := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "def run") {
			bodyStart = i + 1
			break
		}
	}

	if bodyStart == -1 || bodyStart >= len(lines) {
		return code
	}

	bodyLines := lines[bodyStart:]
	dedented := make([]string, 0, len(bodyLines))
	for _, line := range bodyLines {
		if strings.HasPrefix(line, "    ") {
			dedented = append(dedented, line[4:])
		} else if strings.HasPrefix(line, "\t") {
			dedented = append(dedented, line[1:])
		} else {
			dedented = append(dedented, line)
		}
	}

	return strings.Join(dedented, "\n")
}

// indentCode adds four spaces to every non-empty line, producing a block
// suitable for embedding as the body of a Snowflake Python stored procedure.
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

// rewritePythonTableRefs replaces bare table names in Python code with the
// role-appropriate view names and, when the permission includes a RowAccess
// policy, appends a Snowpark .where() filter to every session.table() call.
func rewritePythonTableRefs(role, code, userName string) string {
	globalPolicyMu.RLock()
	policy := globalPolicy
	globalPolicyMu.RUnlock()

	if policy == nil {
		return code
	}

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

	if perm.RowAccess != nil {
		rewritten = injectPythonRowFilter(rewritten, perm.RowAccess, userName)
	}

	return rewritten
}

// injectPythonRowFilter appends a Snowpark .where() call to every
// session.table() expression in code, restricting rows to those the user is
// authorised to see.
//
// The filter is expressed as a SQL string passed to .where(), which Snowpark
// pushes down to the warehouse. Column scoping is already handled by the view;
// this filter addresses row-level access only.
//
// Note: this approach rewrites at the source level by locating the closing
// parenthesis of each .table(...) call and inserting the .where(...) chain
// immediately after. It does not parse the AST, so nested or multi-line
// .table() calls may not be handled correctly.
func injectPythonRowFilter(code string, rowAccess *RowAccess, userName string) string {
	filterExpr := fmt.Sprintf(
		`.where("%s IN (SELECT %s FROM %s WHERE %s = '%s')")`,
		rowAccess.TargetColumn,
		rowAccess.DataColumn,
		rowAccess.FilterTable,
		rowAccess.UserColumn,
		userName,
	)

	lines := strings.Split(code, "\n")
	for i, line := range lines {
		if !strings.Contains(line, ".table(") {
			continue
		}
		tableIdx := strings.Index(line, ".table(")
		rest := line[tableIdx:]
		closeIdx := strings.Index(rest, ")")
		if closeIdx != -1 {
			insertAt := tableIdx + closeIdx + 1
			lines[i] = line[:insertAt] + filterExpr + line[insertAt:]
		}
	}
	return strings.Join(lines, "\n")
}
