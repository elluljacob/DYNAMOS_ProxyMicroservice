package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	clientv3 "go.etcd.io/etcd/client/v3"
)

const policyEtcdKey = "/policyEnforcer/dataPolicy/EXT-PROVIDER"

type TableConfig struct {
	Visible []string          `json:"visible"`
	Masked  map[string]string `json:"masked"`
}

type RowAccess struct {
	FilterTable  string `json:"filterTable"`
	UserColumn   string `json:"userColumn"`
	DataColumn   string `json:"dataColumn"`
	TargetColumn string `json:"targetColumn"`
}

type Permission struct {
	Assignee  string                 `json:"assignee"`
	RowAccess *RowAccess             `json:"rowAccess"`
	Tables    map[string]TableConfig `json:"tables"`
}
type ODRLPolicy struct {
	Context     string       `json:"@context"`
	Type        string       `json:"@type"`
	UID         string       `json:"uid"`
	Permissions []Permission `json:"permissions"`
}

// Strict ODRL structs
type StrictODRLPermission struct {
	UID      string `json:"uid"`
	Target   string `json:"target"`
	Action   string `json:"action"`
	Assignee string `json:"assignee"`
}

type StrictODRLPolicy struct {
	Context    string                 `json:"@context"`
	Type       string                 `json:"@type"`
	UID        string                 `json:"uid"`
	Permission []StrictODRLPermission `json:"permission"`
}

var (
	globalPolicy   *ODRLPolicy
	globalPolicyMu sync.RWMutex
)

var useStrictODRL = os.Getenv("POLICY_MODE") == "strict"

func LoadPolicy(ctx context.Context, client *clientv3.Client) error {
	resp, err := client.Get(ctx, policyEtcdKey)
	if err != nil {
		return fmt.Errorf("failed to get policy from etcd: %w", err)
	}
	if len(resp.Kvs) == 0 {
		return fmt.Errorf("no policy found at etcd key: %s", policyEtcdKey)
	}

	if err := parseAndSetPolicy(ctx, resp.Kvs[0].Value); err != nil {
		return err
	}

	logger.Sugar().Infof("Policy loaded from etcd key: %s", policyEtcdKey)
	go watchPolicy(ctx, client)
	return nil
}

func watchPolicy(ctx context.Context, client *clientv3.Client) {
	watchChan := client.Watch(ctx, policyEtcdKey)
	logger.Sugar().Infof("Watching for policy changes at: %s", policyEtcdKey)

	for {
		select {
		case <-ctx.Done():
			logger.Info("Policy watcher stopped")
			return
		case watchResp, ok := <-watchChan:
			if !ok {
				logger.Warn("Policy watch channel closed")
				return
			}
			for _, event := range watchResp.Events {
				switch event.Type {
				case clientv3.EventTypePut:
					if err := parseAndSetPolicy(ctx, event.Kv.Value); err != nil {
						logger.Sugar().Errorf("Failed to reload policy: %v", err)
					} else {
						logger.Sugar().Infof("Policy hot-reloaded successfully")
					}
				case clientv3.EventTypeDelete:
					logger.Warn("Policy deleted from etcd — keeping last known policy")
				}
			}
		}
	}
}

func parseAndSetPolicy(ctx context.Context, data []byte) error {
	if useStrictODRL {
		return parseAndSetStrictODRL(ctx, data)
	}

	var policy ODRLPolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		return fmt.Errorf("failed to parse inspired policy JSON: %w", err)
	}
	if err := createViewsFromPolicy(ctx, &policy); err != nil {
		return fmt.Errorf("failed to create views: %w", err)
	}
	globalPolicyMu.Lock()
	globalPolicy = &policy
	globalPolicyMu.Unlock()
	logger.Sugar().Infof("Policy set: %s (%d permissions)", policy.UID, len(policy.Permissions))
	return nil
}

func parseAndSetStrictODRL(ctx context.Context, data []byte) error {
	var strict StrictODRLPolicy
	if err := json.Unmarshal(data, &strict); err != nil {
		return fmt.Errorf("failed to parse strict ODRL JSON: %w", err)
	}

	// Convert to internal format
	// Group column permissions by role and table
	type roleTable struct{ role, table string }
	colMap := make(map[roleTable][]string)

	for _, p := range strict.Permission {
		// Extract role: "urn:dynamos:role:RESEARCHER" -> "RESEARCHER"
		role := urnSuffix(p.Assignee)

		target := p.Target
		if strings.Contains(target, "#") {
			// Column-level: "urn:dynamos:dataset:PERSONEN#INSTCODE"
			parts := strings.SplitN(target, "#", 2)
			table := urnSuffix(parts[0])
			col := parts[1]
			key := roleTable{role, table}
			colMap[key] = append(colMap[key], col)
		}
		// Table-level permissions (no #) grant full access — handled below
	}

	// Build inspired policy
	roleMap := make(map[string]*Permission)
	for key, cols := range colMap {
		if _, ok := roleMap[key.role]; !ok {
			roleMap[key.role] = &Permission{
				Assignee: key.role,
				Tables:   make(map[string]TableConfig),
			}
		}
		roleMap[key.role].Tables[key.table] = TableConfig{
			Visible: cols,
			Masked:  make(map[string]string),
		}
	}

	// Handle table-level permissions (DATA_STEWARD gets all columns via SELECT *)
	for _, p := range strict.Permission {
		if !strings.Contains(p.Target, "#") {
			role := urnSuffix(p.Assignee)
			table := urnSuffix(p.Target)
			if _, ok := roleMap[role]; !ok {
				roleMap[role] = &Permission{
					Assignee: role,
					Tables:   make(map[string]TableConfig),
				}
			}
			// Empty visible means all columns — we use SELECT * in the view
			if _, exists := roleMap[role].Tables[table]; !exists {
				roleMap[role].Tables[table] = TableConfig{
					Visible: []string{},
					Masked:  make(map[string]string),
				}
			}
		}
	}

	inspired := &ODRLPolicy{
		Context: strict.Context,
		Type:    strict.Type,
		UID:     strict.UID,
	}
	for _, perm := range roleMap {
		inspired.Permissions = append(inspired.Permissions, *perm)
	}

	if err := createViewsFromStrictPolicy(ctx, inspired); err != nil {
		return fmt.Errorf("failed to create views from strict ODRL: %w", err)
	}

	globalPolicyMu.Lock()
	globalPolicy = inspired
	globalPolicyMu.Unlock()
	logger.Sugar().Infof("Strict ODRL policy set: %s", strict.UID)
	return nil
}

func urnSuffix(urn string) string {
	parts := strings.Split(urn, ":")
	return parts[len(parts)-1]
}

func createViewsFromStrictPolicy(ctx context.Context, policy *ODRLPolicy) error {
	for _, perm := range policy.Permissions {
		role := strings.ToUpper(perm.Assignee)
		for tableName, tableConfig := range perm.Tables {
			viewName := fmt.Sprintf("%s_%s", tableName, role)

			var ddl string
			if len(tableConfig.Visible) == 0 {
				// Full table access — DATA_STEWARD
				ddl = fmt.Sprintf("CREATE OR REPLACE VIEW %s AS SELECT * FROM %s", viewName, tableName)
			} else {
				cols := make([]string, 0, len(tableConfig.Visible))
				for _, col := range tableConfig.Visible {
					cols = append(cols, strings.ToUpper(col))
				}
				ddl = fmt.Sprintf(
					"CREATE OR REPLACE VIEW %s AS SELECT %s FROM %s",
					viewName,
					strings.Join(cols, ", "),
					tableName,
				)
			}

			logger.Sugar().Infof("Creating view (strict ODRL): %s", viewName)
			if _, _, err := QuerySnowflake(ctx, ddl); err != nil {
				return fmt.Errorf("failed to create view %s: %w", viewName, err)
			}
		}
	}
	return nil
}

// createViewsFromPolicy creates a Snowflake view per role per table
func createViewsFromPolicy(ctx context.Context, policy *ODRLPolicy) error {
	for _, perm := range policy.Permissions {
		role := strings.ToUpper(perm.Assignee)
		for tableName, tableConfig := range perm.Tables {
			viewName := fmt.Sprintf("%s_%s", tableName, role)

			// Build SELECT clause
			cols := make([]string, 0)
			for _, col := range tableConfig.Visible {
				upper := strings.ToUpper(col)
				if maskVal, isMasked := tableConfig.Masked[upper]; isMasked {
					cols = append(cols, fmt.Sprintf("'%s' AS %s", maskVal, upper))
				} else {
					cols = append(cols, upper)
				}
			}
			// Also include masked columns that aren't in visible
			for col, maskVal := range tableConfig.Masked {
				upper := strings.ToUpper(col)
				found := false
				for _, v := range tableConfig.Visible {
					if strings.EqualFold(v, col) {
						found = true
						break
					}
				}
				if !found {
					cols = append(cols, fmt.Sprintf("'%s' AS %s", maskVal, upper))
				}
			}

			ddl := fmt.Sprintf(
				"CREATE OR REPLACE VIEW %s AS SELECT %s FROM %s",
				viewName,
				strings.Join(cols, ", "),
				tableName,
			)

			logger.Sugar().Infof("Creating view: %s", viewName)
			if _, _, err := QuerySnowflake(ctx, ddl); err != nil {
				return fmt.Errorf("failed to create view %s: %w", viewName, err)
			}
		}
	}
	return nil
}

// viewNameForRole returns the view name for a given role and table
func viewNameForRole(role, table string) string {
	return fmt.Sprintf("%s_%s", strings.ToUpper(table), strings.ToUpper(role))
}

// RewriteQuery rewrites table names in the query to use role-appropriate views
func RewriteQuery(role, query, userName string) (string, error) {
	if query == "" {
		return "", fmt.Errorf("empty query")
	}

	globalPolicyMu.RLock()
	policy := globalPolicy
	globalPolicyMu.RUnlock()

	if policy == nil {
		return "", fmt.Errorf("policy not loaded")
	}

	var perm *Permission
	for i, p := range policy.Permissions {
		if strings.EqualFold(p.Assignee, role) {
			perm = &policy.Permissions[i]
			break
		}
	}
	if perm == nil {
		for i, p := range policy.Permissions {
			if strings.EqualFold(p.Assignee, "RESEARCHER") {
				perm = &policy.Permissions[i]
				break
			}
		}
	}
	if perm == nil {
		return "", fmt.Errorf("no policy found for role: %s", role)
	}

	rewritten := query
	for tableName := range perm.Tables {
		view := viewNameForRole(role, tableName)
		rewritten = replaceTableName(rewritten, tableName, view)
	}

	if perm.RowAccess != nil {
		rewritten = injectRowFilter(rewritten, perm.RowAccess, userName, perm)
	}

	logger.Sugar().Debugf("Rewritten query for role %s user %s: %s", role, userName, rewritten)
	return rewritten, nil
}

func getTableAlias(query, tableName string) string {
	upper := strings.ToUpper(query)
	tableUpper := strings.ToUpper(tableName)

	idx := strings.Index(upper, tableUpper)
	if idx == -1 {
		return tableName
	}

	// Get everything after the table name
	rest := strings.TrimSpace(query[idx+len(tableName):])

	// If next word is not a keyword, it's the alias
	words := strings.Fields(rest)
	if len(words) > 0 {
		first := strings.ToUpper(words[0])
		if first != "WHERE" && first != "JOIN" && first != "ON" &&
			first != "LIMIT" && first != "GROUP" && first != "ORDER" &&
			first != "INNER" && first != "LEFT" && first != "RIGHT" && first != "" {
			return words[0]
		}
	}
	return tableName
}

func injectRowFilter(query string, rowAccess *RowAccess, userName string, perm *Permission) string {
	upper := strings.ToUpper(strings.TrimSpace(query))
	limitClause := ""
	innerQuery := strings.TrimRight(strings.TrimSpace(query), ";")

	if idx := strings.LastIndex(upper, "LIMIT"); idx != -1 {
		limitClause = " " + strings.TrimSpace(query[idx:])
		innerQuery = strings.TrimSpace(query[:idx])
	}

	// Find which view is in the query and get its alias
	qualifiedColumn := rowAccess.TargetColumn
	for tableName := range perm.Tables {
		view := viewNameForRole(strings.ToUpper(perm.Assignee), tableName)
		if strings.Contains(upper, strings.ToUpper(view)) {
			alias := getTableAlias(innerQuery, view)
			qualifiedColumn = alias + "." + rowAccess.TargetColumn
			break
		}
	}

	filter := fmt.Sprintf(
		"%s IN (SELECT %s FROM %s WHERE %s = '%s')",
		qualifiedColumn,
		rowAccess.DataColumn,
		rowAccess.FilterTable,
		rowAccess.UserColumn,
		userName,
	)

	upperInner := strings.ToUpper(innerQuery)
	if strings.Contains(upperInner, "WHERE") {
		idx := strings.Index(upperInner, "WHERE")
		return innerQuery[:idx+5] + " " + filter + " AND " + innerQuery[idx+5:] + limitClause
	}
	return innerQuery + " WHERE " + filter + limitClause
}

// replaceTableName does a case-insensitive whole-word replacement of a table name
func replaceTableName(query, table, view string) string {
	// Use word boundaries to avoid replacing partial matches
	// e.g. don't replace PERSONEN inside PERSONEN_RESEARCHER
	upper := strings.ToUpper(query)
	tableUpper := strings.ToUpper(table)
	viewUpper := strings.ToUpper(view)

	var result strings.Builder
	remaining := upper
	original := query
	offset := 0

	for {
		idx := strings.Index(remaining, tableUpper)
		if idx == -1 {
			result.WriteString(original[offset:])
			break
		}

		// Check it's a whole word (not part of a longer identifier like PERSONEN_RESEARCHER)
		end := idx + len(tableUpper)
		before := idx == 0 || !isIdentChar(rune(remaining[idx-1]))
		after := end >= len(remaining) || !isIdentChar(rune(remaining[end]))

		if before && after {
			result.WriteString(original[offset : offset+idx])
			result.WriteString(viewUpper)
			offset += idx + len(tableUpper)
		} else {
			result.WriteString(original[offset : offset+idx+len(tableUpper)])
			offset += idx + len(tableUpper)
		}
		remaining = upper[offset:]
	}

	return result.String()
}

func isIdentChar(r rune) bool {
	return r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

// getRoleForUser looks up a user's role from the EXT-PROVIDER agreement in etcd
func getRoleForUser(userName string) string {
	resp, err := etcdClient.Get(context.Background(), "/policyEnforcer/agreements/EXT-PROVIDER")
	if err != nil || len(resp.Kvs) == 0 {
		logger.Sugar().Warnf("Could not fetch agreement for user %s, defaulting to RESEARCHER", userName)
		return "RESEARCHER"
	}

	var agreement struct {
		Relations map[string]struct {
			Role string `json:"role"`
		} `json:"relations"`
	}

	if err := json.Unmarshal(resp.Kvs[0].Value, &agreement); err != nil {
		logger.Sugar().Warnf("Could not parse agreement, defaulting to RESEARCHER: %v", err)
		return "RESEARCHER"
	}

	if relation, ok := agreement.Relations[userName]; ok && relation.Role != "" {
		logger.Sugar().Infof("User %s resolved to role: %s", userName, relation.Role)
		return relation.Role
	}

	logger.Sugar().Warnf("User %s not found in agreement, defaulting to RESEARCHER", userName)
	return "RESEARCHER"
}
