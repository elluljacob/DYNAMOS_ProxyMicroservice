# Legacy Request Approval Flow

## Overview

This document describes the **legacy** request approval flow in DYNAMOS: the path from a data analyst’s request through the API Gateway, Policy Enforcer, and Orchestrator until a response is returned. The legacy flow uses the **Legacy Validation Strategy**, which validates access via JSON agreements stored in etcd (see `go/cmd/policy-enforcer/service/legacy_validation_strategy.go`).

For the current policy-enforcer design and eFLINT-based validation, see [Policy Enforcer](policy_enforcer.md).

---

## Architecture Summary

- **API Gateway** sends a `requestApproval` message to the Policy Enforcer via RabbitMQ (`policyEnforcer-in`).
- **Policy Enforcer** uses `getValidAgreements` (legacy path) to validate requested data providers against agreements in etcd at `/policyEnforcer/agreements/{steward}`, checks user relations and archetypes, and returns a `validationResponse`.
- **Routing of `validationResponse`**: The message has no `DestinationQueue` field; the sidecar’s `SendValidationResponse` ([rabbit_send.go#L174](../../go/cmd/sidecar/rabbit_send.go#L174)) hardcodes the destination as `orchestrator-in`.
- **Orchestrator** does not merely forward the response. It:
  - Calls `getAuthorizedProviders()` to determine which valid data providers are currently online (etcd `/agents/online/{key}`).
  - Calls `startCompositionRequest()` to select an archetype from policy, load archetype configuration from etcd, and send `CompositionRequest` messages to authorized providers (data and/or compute).
  - Builds the `userTargets` map with DNS endpoints for the client.
- **API Gateway** receives `requestApprovalResponse` and then calls `sendDataToAuthProviders()`, which sends HTTP POST requests **directly to the agents** (not via RabbitMQ), using the format `http://{url}:{port}/agent/v1/{msgType}/{target}`. The response is received in the `select` block in [requests.go](../../go/cmd/api-gateway/requests.go#L82-L84).

---

## End-to-End Request Approval Flow

### Step-by-Step Process

```
┌────────────────────────────────────────────────────────────────────────┐
│                        REQUEST APPROVAL FLOW                           │
└────────────────────────────────────────────────────────────────────────┘

1. [Client/Data Analyst]
   │
   │  HTTP POST /api/v1/requestApproval
   │  Body: { type, user, dataProviders, dataRequest }
   ▼
2. [API Gateway - requestHandler()]
   │  • Parses request body
   │  • Creates RequestApproval protobuf
   │  • Creates response channel in requestApprovalMap[user.Id]
   │  • Sets DestinationQueue = "policyEnforcer-in"
   │
   │  RabbitMQ: requestApproval → policyEnforcer-in
   ▼
3. [Policy Enforcer - handleIncomingMessages()]
   │  • Routes message type "requestApproval"
   │
   │  checkRequestApproval()
   │  ▼
4. [Policy Enforcer - getValidAgreements() / LegacyValidationStrategy]
   │  FOR each dataProvider (steward):
   │    • GET etcd: /policyEnforcer/agreements/{steward}
   │    • IF not found → add to invalidDataproviders
   │    • Get user's relation from agreement
   │    • IF user not in relations → add to invalidDataproviders
   │    • Match user's allowedArchetypes with agreement's archetypes
   │    • Match user's allowedComputeProviders with agreement's computeProviders
   │    • Store in ValidDataproviders and ValidArchetypes
   │
   │  • Set RequestApproved = len(ValidDataproviders) > 0
   │  • Generate Auth token
   │
   │  RabbitMQ: validationResponse → orchestrator-in (hardcoded in sidecar)
   ▼
5. [Orchestrator - handleIncomingMessages()]
   │  • Routes message type "validationResponse"
   │
   │  handleRequestApproval()
   │  ▼
6. [Orchestrator - getAuthorizedProviders()]
   │  FOR each ValidDataprovider:
   │    • GET etcd: /agents/online/{provider}
   │    • IF online → add to authorizedProviders
   │
   │  IF no authorizedProviders → send error response
   │  ▼
7. [Orchestrator - startCompositionRequest()]
   │  • chooseArchetype() - selects best archetype
   │  • GET etcd: /archetypes/{archetype} for config
   │  • Generate jobName
   │
   │  IF computeToData archetype:
   │    • Role = "all"
   │    • Send CompositionRequest to each provider
   │
   │  ELSE (dataThroughTtp):
   │    • Send CompositionRequest(role=dataProvider) to data providers
   │    • Choose TTP (compute provider)
   │    • Send CompositionRequest(role=computeProvider) to TTP
   │
   │  RabbitMQ: compositionRequest → {provider}-in (for each)
   │  ▼
8. [Orchestrator]
   │  Build RequestApprovalResponse:
   │    • authorizedProviders = {name: dns_endpoint}
   │    • jobId
   │    • auth tokens
   │    • user info
   │    • DestinationQueue = "api-gateway-in"
   │
   │  RabbitMQ: requestApprovalResponse → api-gateway-in
   ▼
9. [API Gateway - handleIncomingMessages()]
   │  • Routes message type "requestApprovalResponse"
   │  • Looks up channel in requestApprovalMap[user.Id]
   │  • Sends response to channel
   ▼
10. [API Gateway - requestHandler() select block]
    │  • Receives response from channel
    │  • Adds requestMetadata to dataRequest
    │  • Adds trace context
    │
    │  sendDataToAuthProviders()
    │  ▼
11. [API Gateway]
    │  FOR each authorizedProvider:
    │    HTTP POST http://{dns}:8080/agent/v1/{type}/{target}
    │    Body: dataRequest with metadata
    ▼
12. [Agents]
    │  Process data requests...
    │  Return responses
    ▼
13. [API Gateway]
    │  Aggregate responses
    │  Return to client
    ▼
14. [Client/Data Analyst]
    │  Receives: { jobId, responses[] }
```

---

## Message Types

| Message Type | Source | Destination | Routing |
|--------------|--------|-------------|---------|
| `requestApproval` | API Gateway | Policy Enforcer | `DestinationQueue` field (`policyEnforcer-in`) |
| `validationResponse` | Policy Enforcer | Orchestrator | Hardcoded in sidecar (`orchestrator-in`) |
| `compositionRequest` | Orchestrator | Agents | `DestinationQueue` field (`{agent}-in`) |
| `requestApprovalResponse` | Orchestrator | API Gateway | `RequestMetadata.DestinationQueue` (`api-gateway-in`) |

---

## Key Data Structures

### RequestApproval (API Gateway → Policy Enforcer)

```protobuf
message RequestApproval {
  string type = 1;               // "requestApproval"
  User user = 2;                 // {id, user_name}
  repeated string data_providers = 3;  // ["VU", "UVA"]
  string destination_queue = 4;  // "policyEnforcer-in"
  map<string, bool> options = 5;
}
```

### ValidationResponse (Policy Enforcer → Orchestrator)

```protobuf
message ValidationResponse {
  string type = 1;                                    // "validationResponse"
  string request_type = 2;                            // e.g., "sqlDataRequest"
  map<string, DataProvider> valid_dataproviders = 3;  // validated providers
  repeated string invalid_dataproviders = 4;          // rejected providers
  Auth auth = 5;                                      // generated auth tokens
  User user = 6;
  bool request_approved = 7;
  UserArchetypes valid_archetypes = 8;
  map<string, bool> options = 9;
}
```

### RequestApprovalResponse (Orchestrator → API Gateway)

```protobuf
message RequestApprovalResponse {
  string type = 1;                              // "requestApprovalResponse"
  User user = 2;
  Auth auth = 3;
  map<string, string> authorized_providers = 4; // {name: dns_endpoint}
  string job_id = 5;
  string error = 6;
  RequestMetadata request_metadata = 7;         // contains DestinationQueue
}
```

---

## etcd Key Paths

| Path | Purpose | Example Value |
|------|---------|---------------|
| `/policyEnforcer/agreements/{name}` | Agreement definitions (legacy) | See agreements.json |
| `/agents/online/{name}` | Online agent status and details | `{name, dns, routingKey}` |
| `/archetypes/{name}` | Archetype configurations | Archetype config JSON |

---

## Legacy Validation Strategy (Code Reference)

When the Policy Enforcer uses the legacy strategy, validation is implemented by `LegacyValidationStrategy` in `go/cmd/policy-enforcer/service/legacy_validation_strategy.go`:

- **Agreement source**: JSON agreements from etcd at `/policyEnforcer/agreements/{steward}` via `AgreementRepository`.
- **Validation steps**: For each requested steward, the strategy fetches the agreement, checks that the user appears in `agreement.Relations`, matches `AllowedArchetypes` with `agreement.Archetypes`, and matches `AllowedComputeProviders` with `agreement.ComputeProviders`. If any check fails, the steward is treated as invalid.

---

## Implementation Notes

- **Redundant `agreements` slice**: In the legacy `getValidAgreements` path, the `agreements` slice is only used to check length, which is equivalent to checking `ValidDataproviders`. This can be simplified or removed when refactoring.
- **Hardcoded routing**: `SendValidationResponse` hardcodes `orchestrator-in`. Other messages use a configurable destination; this could be aligned for consistency.
- **Error handling**: Some error paths in `handleRequestApproval` return without sending a response back to the API Gateway, which may lead to client timeouts.
- **Agent requests**: `sendDataToAuthProviders` uses goroutines for parallel HTTP requests to agents, which improves latency.
