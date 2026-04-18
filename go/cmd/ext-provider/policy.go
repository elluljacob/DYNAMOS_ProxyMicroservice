package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	clientv3 "go.etcd.io/etcd/client/v3"
)

const policyEtcdKey = "/policyEnforcer/dataPolicy/EXT-PROVIDER"

type TableConfig struct {
	Visible []string          `json:"visible"`
	Masked  map[string]string `json:"masked"`
}

type Permission struct {
	Assignee string                 `json:"assignee"`
	Tables   map[string]TableConfig `json:"tables"`
}

type ODRLPolicy struct {
	Context     string       `json:"@context"`
	Type        string       `json:"@type"`
	UID         string       `json:"uid"`
	Permissions []Permission `json:"permissions"`
}

var (
	globalPolicy   *ODRLPolicy
	globalPolicyMu sync.RWMutex
)

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
	var policy ODRLPolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		return fmt.Errorf("failed to parse policy JSON: %w", err)
	}

	// Create views in Snowflake for each role
	if err := createViewsFromPolicy(ctx, &policy); err != nil {
		return fmt.Errorf("failed to create views: %w", err)
	}

	globalPolicyMu.Lock()
	globalPolicy = &policy
	globalPolicyMu.Unlock()

	logger.Sugar().Infof("Policy set: %s (%d permissions)", policy.UID, len(policy.Permissions))
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
			if _, err := QuerySnowflake(ctx, ddl); err != nil {
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
func RewriteQuery(role, query string) (string, error) {
	if query == "" {
		return "", fmt.Errorf("empty query")
	}

	globalPolicyMu.RLock()
	policy := globalPolicy
	globalPolicyMu.RUnlock()

	if policy == nil {
		return "", fmt.Errorf("policy not loaded")
	}

	// Find the permission for this role
	var perm *Permission
	for i, p := range policy.Permissions {
		if strings.EqualFold(p.Assignee, role) {
			perm = &policy.Permissions[i]
			break
		}
	}
	// Fallback to RESEARCHER
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

	logger.Sugar().Debugf("Rewritten query for role %s: %s", role, rewritten)
	return rewritten, nil
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
