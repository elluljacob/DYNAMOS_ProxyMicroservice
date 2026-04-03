# Legacy Policy Update Flow

> **Branch:** `legacy-policy-enforcer`
>
> This document describes the **old/legacy** policy update flow as it existed before recent changes. It complements the [Legacy Request Approval Flow](./legacy_request_approval_flow.md) documentation.

## Overview

The **Policy Update** flow is triggered when an agreement (contract between a data steward and users) is modified via the orchestrator's HTTP API. When an agreement changes, the system must re-evaluate all active jobs that depend on that agreement, because the allowed archetypes, compute providers, or user relations may have changed. This could mean jobs need to switch archetype (e.g., from `dataThroughTtp` to `computeToData`), change their compute provider, or be deleted entirely.

The flow involves a round-trip message exchange:
1. **Orchestrator** detects the agreement change and sends a `policyUpdate` message to the **Policy Enforcer**
2. **Policy Enforcer** re-validates the agreements and sends the `policyUpdate` back with a `ValidationResponse`
3. **Orchestrator** processes the response and updates the job configurations in etcd

## Components Involved

| Component | Role | Key Files |
|-----------|------|-----------|
| **External Client** | Triggers the flow via HTTP PUT | N/A |
| **Orchestrator** | HTTP API, job management, archetype selection | `go/cmd/orchestrator/api.go`, `manage_jobs.go`, `consume.go`, `composition_request.go`, `get_authorized_providers.go`, `main.go` |
| **Sidecar** (Orchestrator's) | gRPC-to-RabbitMQ bridge | `go/cmd/sidecar/rabbit_send.go` |
| **RabbitMQ** | Message broker | Queues: `policyEnforcer-in`, `orchestrator-in` |
| **Sidecar** (Policy Enforcer's) | gRPC-to-RabbitMQ bridge | `go/cmd/sidecar/rabbit_send.go` |
| **Policy Enforcer** | Agreement validation | `go/cmd/policy-enforcer/consume.go`, `policy_update.go`, `generate_validation_response.go` |
| **etcd** | Configuration & state store | Agreements, jobs, agents, archetypes |

## Key Data Structures

### Proto Messages (`proto-files/rabbitMQ.proto`)

**PolicyUpdate:**
```protobuf
message PolicyUpdate {
  string type = 1;
  User user = 2;
  repeated string data_providers = 3;
  RequestMetadata request_metadata = 4;
  ValidationResponse validation_response = 5;
}
```

**ValidationResponse:**
```protobuf
message ValidationResponse {
  string type = 1;
  string request_type = 2;
  map<string, DataProvider> valid_dataproviders = 3;
  repeated string invalid_dataproviders = 4;
  Auth auth = 5;
  User user = 6;
  bool request_approved = 7;
  UserArchetypes valid_archetypes = 8;
  map<string,bool> options = 9;
}
```

### Go Models

**Agreement** (`go/pkg/api/http.go`):
```go
type Agreement struct {
    Name             string              `json:"name"`
    Relations        map[string]Relation `json:"relations"`
    ComputeProviders []string            `json:"computeProviders"`
    Archetypes       []string            `json:"archetypes"`
}

type Relation struct {
    ID                      string   `json:"ID"`
    RequestTypes            []string `json:"requestTypes"`
    DataSets                []string `json:"dataSets"`
    AllowedArchetypes       []string `json:"allowedArchetypes"`
    AllowedComputeProviders []string `json:"allowedComputeProviders"`
}
```

**Archetype** (`go/pkg/api/http.go`):
```go
type Archetype struct {
    Name            string `json:"name"`
    ComputeProvider string `json:"computeProvider"`
    ResultRecipient string `json:"resultRecipient"`
    Weight          int    `json:"weight"`
}
```

## etcd Key Paths

| Path | Content | Used By |
|------|---------|---------|
| `/policyEnforcer/agreements/{stewardName}` | Agreement JSON | Policy Enforcer (read), Orchestrator (write) |
| `/agents/jobs/{agentName}/{userName}/{jobName}` | CompositionRequest JSON (job info) | Orchestrator (read/write/delete) |
| `/agents/online/{agentName}` | AgentDetails JSON | Orchestrator (read) |
| `/archetypes/{archetypeName}` | Archetype JSON | Orchestrator (read) |
| `/agents/jobs/{agentName}/queueInfo/{localJobName}` | Queue info | Orchestrator (delete on cleanup) |

## Detailed Flow

### Phase 1: Trigger - Agreement Update via HTTP

**File:** `go/cmd/orchestrator/api.go` — `agreementsHandler`

1. An external client sends an **HTTP PUT** request to `/api/v1/policyEnforcer/agreements` with an updated `Agreement` JSON body.
2. The `agreementsHandler` function:
   - Saves the updated agreement to etcd at `/policyEnforcer/agreements/{name}` using `api.GenericPutToEtcd`
   - Spawns a **goroutine** `go checkJobs(agreement)` to asynchronously evaluate the impact on active jobs

### Phase 2: Orchestrator - Evaluate Active Jobs

**File:** `go/cmd/orchestrator/manage_jobs.go` — `checkJobs`

3. `checkJobs(agreement)` iterates over each **relation** (user) in the updated agreement:
   - Looks up active job names for this user at this data steward from etcd: `/agents/jobs/{agreementName}/{relationName}`
   - If **no active jobs** exist → returns early, nothing to update
   - If the user has **no allowed archetypes** in the new agreement (empty or `[""]`) → calls `deleteJobInfo` to clean up all related job entries across agents
   - Otherwise → calls `evaluateArchetypeInActiveJobs`

**File:** `go/cmd/orchestrator/manage_jobs.go` — `evaluateArchetypeInActiveJobs`

4. For each active job name:
   - Retrieves the current job's `CompositionRequest` from etcd
   - Creates a `PolicyUpdate` protobuf message:
     - `Type`: `"policyUpdate"`
     - `User`: ID and username from the relation
     - `RequestMetadata.DestinationQueue`: `"policyEnforcer-in"`
     - `RequestMetadata.CorrelationId`: newly generated UUID
   - Calls `getJobAcrossAgents` to discover **all agents** that participate in this job (scanning `/agents/online/` and looking up each agent's job entry)
   - Populates `DataProviders` with agents whose role is `"all"` or `"dataProvider"` (these are the agents whose agreements need re-validation)
   - **Stores** the `agentsWithThisJob` map in the in-memory `policyUpdateMap`, keyed by correlation ID (for later retrieval when the response comes back)
   - Sends the `PolicyUpdate` via `c.SendPolicyUpdate` (gRPC call to the sidecar)

### Phase 3: Sidecar - Publish to RabbitMQ

**File:** `go/cmd/sidecar/rabbit_send.go` — `SendPolicyUpdate`

5. The orchestrator's sidecar:
   - Marshals the `PolicyUpdate` protobuf to bytes
   - Creates an AMQP `Publishing` message with the correlation ID and type `"policyUpdate"`
   - Publishes to the destination queue (`policyEnforcer-in`) via RabbitMQ

### Phase 4: Policy Enforcer - Validate Agreements

**File:** `go/cmd/policy-enforcer/consume.go` — `handleIncomingMessages`

6. The policy enforcer's sidecar delivers the message. The `handleIncomingMessages` function dispatches `"policyUpdate"` type messages to `checkPolicyUpdate`.

**File:** `go/cmd/policy-enforcer/policy_update.go` — `checkPolicyUpdate`

7. `checkPolicyUpdate(ctx, policyUpdate)`:
   - Creates a `ValidationResponse` with `Type: "policyUpdate"` and the user info
   - Calls `getValidAgreements` (same function used in the request approval flow)

**File:** `go/cmd/policy-enforcer/generate_validation_response.go` — `getValidAgreements`

8. `getValidAgreements` iterates over each data provider in the update:
   - Looks up the agreement from etcd at `/policyEnforcer/agreements/{steward}`
   - If steward not found → adds to `invalidDataproviders`
   - Checks if the **user** exists in the agreement's `Relations` map
   - If user not found → adds to `invalidDataproviders`
   - Matches user's `AllowedArchetypes` against agreement's `Archetypes` using `lib.GetMatchedElements` (intersection)
   - If no matching archetypes → adds to `invalidDataproviders`
   - Otherwise:
     - Adds the steward to `ValidDataproviders` with matched archetypes
     - Adds matched compute providers
     - Adds to valid agreements list

9. Back in `checkPolicyUpdate`:
   - Sets `DestinationQueue` to `"orchestrator-in"`
   - Attaches the `ValidationResponse` to the `PolicyUpdate`
   - If **no agreements or valid providers** → logs warning and sends the update (with `RequestApproved: false`)
   - Sets `RequestApproved = len(ValidDataproviders) > 0`
   - Sends the `PolicyUpdate` back via `c.SendPolicyUpdate` (through its own sidecar to RabbitMQ)

> **Note:** There is a potential bug here — when no agreements exist, the function sends the update and then continues to set `RequestApproved` and send again, resulting in a **duplicate message**.

### Phase 5: Orchestrator - Process Policy Update Response

**File:** `go/cmd/orchestrator/consume.go` — `handleIncomingMessages`

10. The orchestrator receives the `"policyUpdate"` message on `orchestrator-in`:
    - Acquires the `policyUpdateMutex`
    - Looks up `agentsWithThisJob` from `policyUpdateMap` by `CorrelationId`
    - Deletes the entry from the map
    - Calls `processPolicyUpdate(ctx, agentsWithThisJob, policyUpdate)`

**File:** `go/cmd/orchestrator/manage_jobs.go` — `processPolicyUpdate`

11. `processPolicyUpdate`:
    - Calls `getAuthorizedProviders` to check which validated data providers are currently **online** (by looking up `/agents/online/{name}` in etcd)
    - Calls `chooseArchetype` to select the best archetype based on the updated validation response
    - Retrieves the archetype configuration from etcd (`/archetypes/{archetype}`)

12. For each agent in the job, the function applies the archetype transition logic:

    **If new archetype is `computeToData`** (`archetypeConfig.ComputeProvider != "other"`):
    - Agents with role `"computeProvider"` → **delete** their job entry from etcd (no longer needed)
    - Agents with role `"all"` or `"dataProvider"` → update to role `"all"` with new archetype ID, save to etcd

    **If new archetype is `dataThroughTtp`** (`archetypeConfig.ComputeProvider == "other"`):
    - Chooses a third-party compute provider via `chooseThirdParty` (finds intersection of allowed compute providers across all valid data providers, picks the first one that is online)
    - If current compute provider matches the chosen TTP → keep it
    - If current compute provider is different → **delete** its job entry
    - Agents with role `"all"` → check if still valid, update to role `"dataProvider"` with new archetype, or delete if no longer valid

13. If **no compute provider was already assigned** for the new archetype:
    - Creates a new `CompositionRequest` for the TTP with role `"computeProvider"`
    - Sends it via `c.SendCompositionRequest` to the TTP's routing key

## Archetype Selection Logic

**File:** `go/cmd/orchestrator/composition_request.go` — `chooseArchetype`

The archetype selection is shared between the request approval and policy update flows:

1. If user `Options` are provided:
   - `aggregate: true` → prefer `dataThroughTtp` if all providers allow it
   - `aggregate: false` with multiple providers → prefer `computeToData` if all providers allow it
2. If no option-based selection → pick archetype with lowest `Weight` from etcd
3. If that's not allowed by all providers → fall back to first available archetype from any provider

## Comparison with Request Approval Flow

| Aspect | Request Approval | Policy Update |
|--------|-----------------|---------------|
| **Trigger** | User data request via API Gateway | Agreement updated via HTTP PUT |
| **Initiator** | API Gateway → Orchestrator | Orchestrator (self-triggered) |
| **Policy Enforcer input** | `RequestApproval` message | `PolicyUpdate` message |
| **Policy Enforcer output** | `ValidationResponse` (via `SendValidationResponse`) | `PolicyUpdate` with embedded `ValidationResponse` (via `SendPolicyUpdate`) |
| **Orchestrator response** | Creates new jobs, sends `RequestApprovalResponse` to user | Modifies/deletes existing jobs, may send new `CompositionRequest` |
| **Shared logic** | `getValidAgreements`, `chooseArchetype`, `getAuthorizedProviders`, `chooseThirdParty` | Same |
| **Auth token** | Generated | Not generated |
| **Correlation** | Via request channels | Via `policyUpdateMap` + correlation ID |

## Summary Flow

```
External Client
    │
    │ HTTP PUT /api/v1/policyEnforcer/agreements
    ▼
Orchestrator (api.go: agreementsHandler)
    │
    ├─ Save agreement to etcd
    │
    └─ go checkJobs(agreement)
        │
        ├─ No active jobs? → done
        ├─ No allowed archetypes? → deleteJobInfo → done
        │
        └─ evaluateArchetypeInActiveJobs
            │
            ├─ Build PolicyUpdate message
            ├─ Store job context in policyUpdateMap
            │
            └─ c.SendPolicyUpdate → Sidecar → RabbitMQ (policyEnforcer-in)
                                                    │
                                                    ▼
                                            Policy Enforcer (consume.go)
                                                    │
                                                    └─ checkPolicyUpdate
                                                        │
                                                        ├─ getValidAgreements (from etcd)
                                                        ├─ Attach ValidationResponse
                                                        │
                                                        └─ c.SendPolicyUpdate → Sidecar → RabbitMQ (orchestrator-in)
                                                                                                │
                                                                                                ▼
                                                                                        Orchestrator (consume.go)
                                                                                                │
                                                                                                └─ processPolicyUpdate
                                                                                                    │
                                                                                                    ├─ getAuthorizedProviders
                                                                                                    ├─ chooseArchetype
                                                                                                    ├─ Get archetype config
                                                                                                    │
                                                                                                    ├─ Update job roles in etcd
                                                                                                    ├─ Delete obsolete jobs
                                                                                                    │
                                                                                                    └─ (if needed) SendCompositionRequest to new TTP
```

## Related Documentation

- [Legacy Request Approval Flow](./legacy_request_approval_flow.md)
- [Old Policy Update Diagrams](../diagrams/old_policy_update_diagrams.md)
- [Old Policy Update C4 Diagrams](../diagrams/old_policy_update_c4.md)
