# Policy Format Reference

ext-provider supports two policy modes. The active mode is set via the 
`POLICY_MODE` environment variable in the Helm chart deployment.

---

## ODRL-Inspired Mode (default: `POLICY_MODE=inspired`)

This is the primary format used in the thesis. It extends the ODRL 
Information Model with domain-specific fields for column masking and 
row-level access control.

### Format

```json
{
  "@context": "http://www.w3.org/ns/odrl.jsonld",
  "@type": "Policy",
  "uid": "dynamos-ext-provider-001",
  "permissions": [
    {
      "assignee": "RESEARCHER",
      "rowAccess": {
        "filterTable": "USER_ACCESS",
        "userColumn": "EMAIL",
        "dataColumn": "INSTCODE",
        "targetColumn": "INSTCODE"
      },
      "tables": {
        "PERSONEN": {
          "visible": ["INSTCODE", "UNIEKNR", "GESLACHT", "PEILDAT"],
          "masked": { "GEBDAT": "****", "NATIONALITEIT": "****" }
        }
      }
    },
    {
      "assignee": "DATA_STEWARD",
      "rowAccess": null,
      "tables": {
        "PERSONEN": {
          "visible": ["INSTCODE", "UNIEKNR", "GESLACHT", "GEBDAT", 
                      "NATIONALITEIT", "BKO", "SKO", "PEILDAT"],
          "masked": {}
        }
      }
    }
  ]
}
```

### Fields

| Field | Description |
|-------|-------------|
| `assignee` | Role name, matched against the user's role in the etcd agreement |
| `rowAccess` | Row-level filter config. Set to `null` for unrestricted row access |
| `rowAccess.filterTable` | Snowflake table mapping users to partitions |
| `rowAccess.userColumn` | Column in filterTable containing user identifiers |
| `rowAccess.dataColumn` | Column in filterTable containing partition values |
| `rowAccess.targetColumn` | Column in the data table to filter on |
| `tables` | Map of table names to column visibility config |
| `visible` | Columns included in the role-scoped Snowflake view |
| `masked` | Columns included but with values replaced by the given placeholder |

### Enforcement

At startup and on every policy update:
- A Snowflake view is created for each role/table combination
  e.g. `PERSONEN_RESEARCHER`, `PERSONEN_DATA_STEWARD`
- Visible columns are selected directly
- Masked columns are included as literals: `'****' AS GEBDAT`

At request time:
- Table names in the query are rewritten to role-scoped views
- A `WHERE` subquery is injected for roles with `rowAccess` defined

### Updating the policy at runtime

```bash
kubectl exec -it etcd-0 -n core -c etcd -- etcdctl put \
  /policyEnforcer/dataPolicy/EXT-PROVIDER \
  "$(cat configuration/snowflake_policy/agreements.json)"
```

The proxy will detect the change via its etcd watcher and hot-reload 
the policy and regenerate all views without restart.

---

## Strict ODRL Mode (`POLICY_MODE=strict`)

This mode uses a standards-compliant ODRL policy using only base ODRL 
vocabulary terms. Column-level access is expressed via fragment-identified 
target URIs. Masking and row-level filtering are not supported.

### Format

```json
{
  "@context": "http://www.w3.org/ns/odrl.jsonld",
  "@type": "Set",
  "uid": "urn:dynamos:policies:ext-provider-001",
  "permission": [
    {
      "uid": "urn:dynamos:perm:researcher:personen:instcode",
      "target": "urn:dynamos:dataset:PERSONEN#INSTCODE",
      "action": "read",
      "assignee": "urn:dynamos:role:RESEARCHER"
    },
    {
      "uid": "urn:dynamos:perm:datasteward:personen",
      "target": "urn:dynamos:dataset:PERSONEN",
      "action": "read",
      "assignee": "urn:dynamos:role:DATA_STEWARD"
    }
  ]
}
```

### Rules

- Each column the RESEARCHER may access requires a separate permission entry
  with a fragment identifier: `urn:dynamos:dataset:TABLE#COLUMN`
- A table-level permission (no fragment) grants `SELECT *` on that table
- Columns with no permission entry are absent from the generated view entirely
- No masking is applied — absence of permission means absence of access
- No row filtering is applied in strict mode

### Limitations

Strict ODRL cannot express:
- Data masking (returning a placeholder value instead of the real value)
- Row-level access control based on runtime user identity lookups

For these features use the ODRL-inspired mode.

---

## Toggling between modes

In `charts/ext-provider/values.yaml` or directly in the deployment:

```yaml
env:
  - name: POLICY_MODE
    value: "inspired"   # or "strict"
```

Redeploy after changing:

```bash
helm upgrade -i ext-provider charts/ext-provider \
  -f charts/ext-provider/values.yaml \
  --set branchNameTag=your-branch-name
```

Make sure the policy you want to load is named `agreements.json`.