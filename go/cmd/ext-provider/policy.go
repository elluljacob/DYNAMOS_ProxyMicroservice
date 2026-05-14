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

// policyEtcdKey is the etcd key under which the active data policy is stored.
const policyEtcdKey = "/policyEnforcer/dataPolicy/EXT-PROVIDER"

// TableConfig describes which columns a role may see in plain text and which
// must be replaced with a static mask value.
type TableConfig struct {
	Visible []string          `json:"visible"`
	Masked  map[string]string `json:"masked"`
}

// RowAccess defines a sub-query filter that restricts the rows a user can
// retrieve. The enforcer rewrites queries to add a WHERE clause of the form:
//
//	<TargetColumn> IN (SELECT <DataColumn> FROM <FilterTable> WHERE <UserColumn> = '<user>')
type RowAccess struct {
	FilterTable  string `json:"filterTable"`
	UserColumn   string `json:"userColumn"`
	DataColumn   string `json:"dataColumn"`
	TargetColumn string `json:"targetColumn"`
}

// Permission maps a single assignee (role) to the tables it may access and any
// optional row-level filter that applies to that role.
type Permission struct {
	Assignee  string                 `json:"assignee"`
	RowAccess *RowAccess             `json:"rowAccess"`
	Tables    map[string]TableConfig `json:"tables"`
}

// ODRLPolicy is the internal (inspired) policy representation used by the
// enforcer. It is either parsed directly from etcd or converted from a strict
// ODRL document.
type ODRLPolicy struct {
	Context     string       `json:"@context"`
	Type        string       `json:"@type"`
	UID         string       `json:"uid"`
	Permissions []Permission `json:"permissions"`
}

// StrictODRLPermission represents a single permission entry in a standards-
// compliant ODRL policy document.
type StrictODRLPermission struct {
	UID      string `json:"uid"`
	Target   string `json:"target"`
	Action   string `json:"action"`
	Assignee string `json:"assignee"`
}

// StrictODRLPolicy is a standards-compliant ODRL policy document as stored in
// etcd when POLICY_MODE=strict.
type StrictODRLPolicy struct {
	Context    string                 `json:"@context"`
	Type       string                 `json:"@type"`
	UID        string                 `json:"uid"`
	Permission []StrictODRLPermission `json:"permission"`
}

var (
	// globalPolicy is the active policy used to rewrite queries.  It is
	// replaced atomically on every hot-reload.
	globalPolicy   *ODRLPolicy
	globalPolicyMu sync.RWMutex
)

// useStrictODRL selects the strict ODRL parser when POLICY_MODE=strict.
// When unset the enforcer uses the inspired (internal) policy format.
var useStrictODRL = os.Getenv("POLICY_MODE") == "strict"

// LoadPolicy fetches the policy from etcd, applies it, and starts a background
// watcher that hot-reloads the policy whenever the etcd key changes.
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

// watchPolicy blocks on the etcd watch channel and reloads the policy each
// time the key is updated. A delete event is treated as a no-op so that the
// last known policy continues to be enforced until a replacement is written.
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

// parseAndSetPolicy dispatches to the appropriate parser based on POLICY_MODE
// and atomically replaces the global policy on success.
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

// parseAndSetStrictODRL parses a standards-compliant ODRL document and converts
// it to the internal policy format before applying it.
//
// Column-level targets are identified by the presence of a '#' separator
// (e.g. "urn:dynamos:dataset:PERSONEN#INSTCODE"). Table-level targets (no '#')
// grant full access to that table and are rendered as SELECT *.
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

	// Roles with a table-level target (no column qualifier) receive SELECT *
	// access.  An empty Visible slice signals this in createViewsFromStrictPolicy.
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

// urnSuffix returns the last colon-delimited segment of a URN.
// For example, "urn:dynamos:role:RESEARCHER" returns "RESEARCHER".
func urnSuffix(urn string) string {
	parts := strings.Split(urn, ":")
	return parts[len(parts)-1]
}

// createViewsFromStrictPolicy creates one Snowflake view per role per table for
// a policy that was converted from strict ODRL. When Visible is empty the view
// selects all columns (SELECT *), which is used for roles with table-level
// access such as DATA_STEWARD.
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

// createViewsFromPolicy creates one Snowflake view per role per table from an
// inspired (internal) policy. Masked columns are emitted as string literals
// rather than the underlying column value, preserving the column name in the
// view schema while hiding the data.
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
			// Include masked columns that were not listed in Visible.
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

// viewNameForRole returns the Snowflake view name for the given role and base
// table, following the convention <TABLE>_<ROLE>.
func viewNameForRole(role, table string) string {
	return fmt.Sprintf("%s_%s", strings.ToUpper(table), strings.ToUpper(role))
}

// RewriteQuery replaces bare table names in query with the role-appropriate
// view names and, when the permission includes a RowAccess policy, injects a
// WHERE clause that limits results to rows the user is authorised to see.
//
// If no permission is found for the given role the function falls back to the
// RESEARCHER permission. An error is returned only when no matching permission
// exists at all.
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

// getTableAlias returns the alias used for tableName in query, or tableName
// itself when no alias is present. It works by finding the table reference and
// checking whether the next token is a SQL keyword; if not, that token is the
// alias.
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

// injectRowFilter wraps query with a sub-select filter derived from rowAccess.
// Any existing LIMIT clause is preserved and re-appended after the filter.
// The filter column is qualified with the table alias found in the query to
// avoid ambiguity in joins.
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

// replaceTableName performs a case-insensitive, whole-word replacement of table
// with view inside query. It avoids replacing substrings that are part of a
// longer identifier (e.g. it will not replace PERSONEN inside PERSONEN_RESEARCHER).
func replaceTableName(query, table, view string) string {
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

// isIdentChar reports whether r is a valid SQL identifier character.
func isIdentChar(r rune) bool {
	return r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

// getRoleForUser looks up the role assigned to userName in the EXT-PROVIDER
// agreement stored in etcd. If the agreement cannot be read or the user is not
// listed, it defaults to RESEARCHER.
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
