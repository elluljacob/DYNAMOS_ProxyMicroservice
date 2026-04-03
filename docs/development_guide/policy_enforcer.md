# Policy Enforcer — Technical Documentation

## Table of Contents

- [1. Overview](#1-overview)
- [2. Request Approval Flow](#2-request-approval-flow)
  - [2.1 High-Level Flow](#21-high-level-flow)
  - [2.2 Legacy Validation Path](#22-legacy-validation-path-json-agreements)
  - [2.3 eFLINT Validation Path](#23-eflint-validation-path)
  - [2.4 Policy Update Flow](#24-policy-update-flow)
- [3. RabbitMQ Messaging](#3-rabbitmq-messaging)
  - [3.1 Consumed Messages](#31-consumed-messages)
  - [3.2 Published Messages](#32-published-messages)
  - [3.3 Message Routing Summary](#33-message-routing-summary)
- [4. HTTP API Endpoints](#4-http-api-endpoints)
  - [4.1 Health](#41-health)
  - [4.2 Policy Enforcer API (Reasoner-Agnostic)](#42-policy-enforcer-api-reasoner-agnostic)
  - [4.3 eFLINT Instance Management API](#43-eflint-instance-management-api)
  - [4.4 eFLINT Pool Management API](#44-eflint-pool-management-api)
  - [4.5 eFLINT State Management API (POC)](#45-eflint-state-management-api-poc)
- [5. Software Architecture](#5-software-architecture)
  - [5.1 Package Structure](#51-package-structure)
  - [5.2 Key Interfaces and Structs](#52-key-interfaces-and-structs)
  - [5.3 Strategy Pattern — Validation Strategies](#53-strategy-pattern--validation-strategies)
  - [5.4 Reasoner Abstraction](#54-reasoner-abstraction)
  - [5.5 Repository Pattern](#55-repository-pattern)
- [6. eFLINT Instance Pool](#6-eflint-instance-pool)
  - [6.1 Architecture](#61-architecture)
  - [6.2 Pool Lifecycle](#62-pool-lifecycle)
  - [6.3 Instance States](#63-instance-states)
  - [6.4 Health Monitoring](#64-health-monitoring)
  - [6.5 State Management](#65-state-management)
  - [6.6 eFLINT Commands](#66-eflint-commands)
- [7. Configuration](#7-configuration)
  - [7.1 Local Configuration](#71-local-configuration)
  - [7.2 Production Configuration](#72-production-configuration)
  - [7.3 etcd Key Paths](#73-etcd-key-paths)
  - [7.4 Provider Validation Configs](#74-provider-validation-configs)
- [8. Key Data Structures (Protobuf)](#8-key-data-structures-protobuf)
- [9. Deployment](#9-deployment)
- [10. Diagrams](#10-diagrams)

---

## 1. Overview

The **Policy Enforcer** is a core DYNAMOS microservice responsible for validating whether a data analyst's request to access data from one or more data providers should be approved. It sits between the API Gateway and the Orchestrator in the request approval pipeline.

The current Policy Enforcer supports **two validation strategies**:

| Strategy | Description | Configuration Source |
|----------|-------------|---------------------|
| **Legacy** | Validates against JSON agreement documents stored in etcd | `/policyEnforcer/agreements/{provider}` |
| **eFLINT** | Validates against formal eFLINT policy models using a dedicated eFLINT reasoner server | `/policyEnforcer/eflintModels/{provider}` |

The strategy used for each data provider is determined at runtime by consulting the provider's configuration in etcd. This means different providers in the same request can be validated using different strategies — one provider might use the legacy JSON approach while another uses the eFLINT reasoner.

### Key Capabilities

- **Dual-strategy validation** via the Strategy pattern
- **Concurrent provider validation** using goroutines
- **eFLINT instance pooling** for high-throughput stateless validation
- **Reasoner-agnostic HTTP API** for querying allowed clauses and validating requests
- **eFLINT management HTTP API** for instance lifecycle, pool management, and state inspection
- **Repository pattern** for abstracting etcd storage access
- **Health monitoring** with automatic unhealthy instance replacement

---

## 2. Request Approval Flow

### 2.1 High-Level Flow

The request approval flow spans multiple DYNAMOS services. The Policy Enforcer's role is step 3 in this pipeline:

```
1. [Data Analyst]
   │  HTTP POST /api/v1/requestApproval
   ▼
2. [API Gateway]
   │  Publishes requestApproval → RabbitMQ (policyEnforcer-in)
   ▼
3. [Policy Enforcer]               ◀── THIS SERVICE
   │  Validates each data provider (legacy or eFLINT)
   │  Publishes validationResponse → RabbitMQ (orchestrator-in)
   ▼
4. [Orchestrator]
   │  Checks provider availability, chooses archetype
   │  Sends compositionRequests to agents
   │  Publishes requestApprovalResponse → RabbitMQ (api-gateway-in)
   ▼
5. [API Gateway]
   │  Sends data requests directly to authorised agents via HTTP
   ▼
6. [Data Analyst]
      Receives aggregated response
```

### 2.2 Detailed Policy Enforcer Flow

When a `requestApproval` message arrives on the `policyEnforcer-in` RabbitMQ queue, the following happens:

```
┌─────────────────────────────────────────────────────────────────┐
│                   POLICY ENFORCER INTERNAL FLOW                  │
└─────────────────────────────────────────────────────────────────┘

1. [consume.go] handleIncomingMessages()
   │  Routes message by type: "requestApproval"
   ▼
2. [consume.go] handleRequestApproval()
   │  Unmarshals pb.RequestApproval
   │  Calls ValidationService.ValidateRequest()
   ▼
3. [service/validation_service.go] ValidateRequest()
   │  Builds initial ValidationResponse
   │  Launches goroutines for each data provider (concurrent)
   ▼
4. [service/validation_service.go] validateSingleProvider()
   │  FOR EACH data provider (in parallel):
   │    ├── resolveStrategy(provider)
   │    │     └── Checks /policyEnforcer/configs/{provider} in etcd
   │    │         ├── "eflint" + available → EflintValidationStrategy
   │    │         └── otherwise            → LegacyValidationStrategy
   │    │
   │    └── strategy.Validate(steward, userName)
   │          ├── [Legacy Path]  → See §2.2
   │          └── [eFLINT Path]  → See §2.3
   ▼
5. [service/validation_service.go] processValidationResults()
   │  Collects results from all goroutines
   │  Populates ValidDataproviders / InvalidDataproviders
   │  Sets RequestApproved = len(ValidDataproviders) > 0
   │  Generates Auth token if approved
   ▼
6. [consume.go]
   │  Sends ValidationResponse via RabbitMQ → orchestrator-in
   ▼
   Done.
```

### 2.2 Legacy Validation Path (JSON Agreements)

When the `LegacyValidationStrategy` is selected:

```
LegacyValidationStrategy.Validate(steward, userName)
  │
  ├── 1. Fetch agreement from etcd
  │      GET /policyEnforcer/agreements/{steward}
  │      → Deserializes JSON into api.Agreement struct
  │
  ├── 2. Check if agreement exists
  │      └── Not found → return invalid ("agreement not found")
  │
  ├── 3. Validate user access
  │      ├── Look up user in agreement.Relations[userName]
  │      │   └── Not found → return invalid ("user not in relations")
  │      │
  │      ├── Match user's AllowedArchetypes ∩ agreement.Archetypes
  │      │   └── No overlap → return invalid ("no matching archetypes")
  │      │
  │      └── Match user's AllowedComputeProviders ∩ agreement.ComputeProviders
  │
  └── 4. Return ValidationResult
         { IsValid: true, MatchedArchetypes, MatchedComputeProvs, UserRelation }
```

**Agreement structure in etcd** (`/policyEnforcer/agreements/{steward}`):

```json
{
  "name": "VU",
  "relations": {
    "user@example.com": {
      "id": "user@example.com",
      "requestTypes": ["sqlDataRequest", "genericRequest"],
      "dataSets": ["wageGap"],
      "allowedArchetypes": ["computeToData", "dataThroughTtp"],
      "allowedComputeProviders": ["SURF"]
    }
  },
  "computeProviders": ["SURF", "otherCompany"],
  "archetypes": ["computeToData", "dataThroughTtp", "reproducableScience"]
}
```

### 2.3 eFLINT Validation Path

When the `EflintValidationStrategy` is selected, it delegates to the `EflintReasoner` which manages the full pool lifecycle:

```
EflintValidationStrategy.Validate(steward, userName)
  │
  └── EflintReasoner.GetAllAllowedClauses(ctx, steward, userName)
        │
        ├── 1. Acquire idle instance from eFLINT pool
        │      └── Blocks up to AcquireTimeout (30s)
        │          Instance marked as "in_use"
        │
        ├── 2. Fetch eFLINT model from etcd
        │      GET /policyEnforcer/eflintModels/{steward}
        │      → Returns raw eFLINT specification text
        │
        ├── 3. Load model into acquired instance
        │      Sends "phrases" command with full model text
        │      → eFLINT server parses and activates the model
        │
        ├── 4. Query allowed clauses
        │      Sends "facts" command
        │      → Parses response JSON
        │      → Filters for "allowed-*" fact types matching the requester
        │         • allowed-archetype(steward, userName)
        │         • allowed-compute-provider(steward, userName)
        │         • allowed-request-type(steward, userName)
        │         • allowed-data-set(steward, userName)
        │
        └── 5. Release instance (async goroutine)
               ├── Send "revert" command (revert to node 1 — initial state)
               ├── Verify empty state via status check
               └── Return instance to pool as "idle"

  Then, back in the strategy:
  ├── No allowed archetypes → return invalid
  └── Otherwise → return valid with matched clauses
```

> **Note:** The same `EflintReasoner` is used by both the RabbitMQ validation flow
> (via `EflintValidationStrategy`) and the HTTP API (via `Enforcer`), ensuring
> consistent pool lifecycle and model loading behaviour.

**eFLINT model example** (simplified):

```eflint
Fact organization Identified by String.
Fact requester Identified by String.
Fact archetype Identified by String.

Fact allowed-archetype Identified by organization * requester * archetype.

+allowed-archetype("VU", "user@example.com", "dataThroughTtp").
```

### 2.4 Policy Update Flow

The Policy Enforcer also handles `policyUpdate` messages, which are sent when agreements change and active jobs need re-validation:

```
1. [consume.go] handleIncomingMessages()
   │  Routes message by type: "policyUpdate"
   ▼
2. [consume.go] handlePolicyUpdate()
   │  Unmarshals pb.PolicyUpdate
   │  Converts to pb.RequestApproval (reuses validation pipeline)
   │  Validates via ValidationService.ValidateRequest()
   │  Embeds ValidationResponse in PolicyUpdate
   │  Sets DestinationQueue = "orchestrator-in"
   │  Sends PolicyUpdate response via RabbitMQ
   ▼
   Done.
```

---

## 3. RabbitMQ Messaging

### 3.1 Consumed Messages

The Policy Enforcer consumes from the **`policyEnforcer-in`** queue.

| Message Type | Protobuf Type | Description |
|-------------|---------------|-------------|
| `"requestApproval"` | `pb.RequestApproval` | Request from API Gateway to validate data access |
| `"policyUpdate"` | `pb.PolicyUpdate` | Request from Orchestrator to re-validate after policy change |

**Queue Configuration:**
- **Service Name:** `policy-enforcer-in`
- **Routing Key:** `policy-enforcer-in`
- **Queue Auto-Delete:** `false`

### 3.2 Published Messages

| Message Type | Protobuf Type | Destination Queue | Description |
|-------------|---------------|-------------------|-------------|
| `"validationResponse"` | `pb.ValidationResponse` | `orchestrator-in` (hardcoded) | Result of request approval validation |
| `"policyUpdate"` | `pb.PolicyUpdate` | `orchestrator-in` (hardcoded) | Result of policy update re-validation |

### 3.3 Message Routing Summary

```
                    policyEnforcer-in                orchestrator-in
                    ┌──────────────┐                 ┌──────────────┐
 API Gateway ──────►│              │                 │              │──────► Orchestrator
                    │   Policy     │ ───────────────►│              │
 Orchestrator ─────►│   Enforcer   │                 │              │
  (policyUpdate)    └──────────────┘                 └──────────────┘
                         ▲                                ▲
                         │                                │
                    consumes from                   publishes to
```

---

## 4. HTTP API Endpoints

The Policy Enforcer exposes an HTTP API on port **8082** (local) or **8080** (production). All endpoints are prefixed with `/api/v1`. The full OpenAPI 3.0.3 specification is available at `go/cmd/policy-enforcer/docs/openapi.yaml`.

### 4.1 Health

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/health` | Health check endpoint (used by Kubernetes probes) |

### 4.2 Policy Enforcer API (Reasoner-Agnostic)

These endpoints provide a **reasoner-agnostic** interface for querying policies. They work regardless of whether the underlying reasoner is eFLINT or another implementation.

Each request follows the same pool lifecycle as the RabbitMQ validation flow: the reasoner **acquires** an idle instance from the pool, **loads** the organization's eFLINT model from etcd via `SendPhrases`, **executes** the query, and **releases** the instance asynchronously.

| Method | Path | Query Params | Description |
|--------|------|-------------|-------------|
| `GET` | `/api/v1/policy-enforcer/allowed-clauses` | `organization`, `requester` | All allowed clauses (request types, data sets, archetypes, compute providers) in a single request |
| `POST` | `/api/v1/policy-enforcer/validate` | — | Validate a full data access request |

**Example — Get all allowed clauses:**

```
GET /api/v1/policy-enforcer/allowed-clauses?organization=VU&requester=user@example.com
```

Response:
```json
{
  "organization": "VU",
  "requester": "user@example.com",
  "request_types": ["sqlDataRequest", "genericRequest"],
  "data_sets": ["wageGap"],
  "archetypes": ["dataThroughTtp"],
  "compute_providers": ["SURF"]
}
```

**Example — Validate a request:**

```
POST /api/v1/policy-enforcer/validate
Content-Type: application/json

{
  "organization": "VU",
  "requester": "user@example.com",
  "request_type": "sqlDataRequest",
  "data_set": "wageGap",
  "archetype": "dataThroughTtp",
  "compute_provider": "SURF"
}
```

Response:
```json
{
  "allowed": true,
  "reason": "",
  "organization": "VU",
  "requester": "user@example.com",
  "request_type": "sqlDataRequest",
  "data_set": "wageGap",
  "archetype": "dataThroughTtp",
  "compute_provider": "SURF"
}
```

### 4.3 eFLINT Instance Management API

These endpoints manage individual eFLINT server instances. They are primarily used for debugging and development.

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/eflint/status` | Get instance status (optional `?instance_id=X` for pool instances) |
| `POST` | `/api/v1/eflint/start` | Start an eFLINT instance with a model (supports `force` flag) |
| `POST` | `/api/v1/eflint/stop` | Stop a running instance |
| `POST` | `/api/v1/eflint/restart` | Restart an instance with its current model |
| `POST` | `/api/v1/eflint/command` | Send a raw command to the eFLINT server |

**Example — Send a command:**

```
POST /api/v1/eflint/command
Content-Type: application/json

{
  "command": "facts"
}
```

### 4.4 eFLINT Pool Management API

These endpoints manage the eFLINT instance pool used for concurrent validation.

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/eflint/instances` | List all pool instances with their state |
| `GET` | `/api/v1/eflint/instances/pool-size` | Get pool size statistics (total, idle, in_use, unhealthy) |
| `PUT` | `/api/v1/eflint/instances/pool-size` | Dynamically resize the pool |

**Example — Get pool statistics:**

```
GET /api/v1/eflint/instances/pool-size
```

Response:
```json
{
  "target_size": 3,
  "total": 3,
  "idle": 2,
  "in_use": 1,
  "unhealthy": 0
}
```

### 4.5 eFLINT State Management API (POC)

These endpoints provide state persistence and checkpoint/restore capabilities. They are a **proof-of-concept** for future stateful eFLINT use cases.

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/eflint/state` | Get current execution graph state |
| `POST` | `/api/v1/eflint/state/export` | Export state for persistence |
| `POST` | `/api/v1/eflint/state/import` | Import a previously exported state |
| `POST` | `/api/v1/eflint/state/checkpoint` | Create a named checkpoint |
| `POST` | `/api/v1/eflint/state/checkpoint/restore` | Restore a named checkpoint |
| `GET` | `/api/v1/eflint/state/checkpoints` | List all checkpoints |
| `DELETE` | `/api/v1/eflint/state/checkpoint/{name}` | Delete a checkpoint |

---

## 5. Software Architecture

### 5.1 Package Structure

```
go/cmd/policy-enforcer/
├── main.go                          # Application bootstrap and dependency wiring
├── consume.go                       # RabbitMQ message handlers
├── routes.go                        # HTTP route registration
├── config_local.go                  # Local development configuration (build tag: local)
├── config_prod.go                   # Production configuration (build tag: !local)
│
├── service/                         # Business logic layer
│   ├── validation_service.go        # Orchestrates validation (concurrent)
│   ├── validation_strategy.go       # Strategy interface + ValidationResult
│   ├── legacy_validation_strategy.go    # Legacy JSON agreement validation
│   ├── eflint_validation_strategy.go    # eFLINT-based validation
│   ├── response_sender.go           # Abstracts RabbitMQ response sending
│   └── auth_token_generator.go      # Auth token generation interface
│
├── repository/                      # Data access layer
│   ├── agreement_repository.go      # Legacy agreement repository (interface)
│   ├── etcd_agreement_repository.go # etcd implementation
│   ├── eflint_model_repository.go   # eFLINT model repository
│   ├── eflint_state_repository.go   # eFLINT state repository
│   └── provider_config_repository.go # Provider config repository
│
├── eflint/                          # eFLINT server management
│   ├── errors.go                    # Error definitions
│   ├── instance.go                  # Single eFLINT process wrapper
│   ├── manager.go                   # Instance lifecycle management (start/stop/command)
│   ├── pool.go                      # Instance pool (acquire/release/health)
│   ├── state_manager.go             # State export/import/checkpoint
│   ├── instance_api.go              # HTTP handlers for instance management
│   ├── state_api.go                 # HTTP handlers for state management
│   └── empty.eflint                 # Empty model for bootstrapping pool instances
│
├── reasoner/                        # Reasoner abstraction
│   ├── reasoner.go                  # Reasoner interface definitions
│   └── eflint_reasoner.go           # eFLINT implementation of Reasoner
│
├── policyenforcer/                  # Policy enforcement logic
│   ├── enforcer.go                  # Core enforcer (delegates to Reasoner)
│   └── types.go                     # Request/response types
│
├── policyenforcerhttp/              # HTTP API handlers
│   └── http_handler.go              # Policy enforcer HTTP handlers
│
├── httpapi/                         # HTTP utility functions
│   └── httpapi.go                   # JSON helpers, method enforcement
│
└── docs/
    └── openapi.yaml                 # OpenAPI 3.0.3 specification
```

### 5.2 Key Interfaces and Structs

#### Application (main.go)

The `Application` struct is the top-level composition root:

```go
type Application struct {
    logger            *slog.Logger
    etcdClient        *etcd.Client
    grpcConn          *grpc.ClientConn
    rabbitClient      pb.SideCarClient
    validationService *service.ValidationService
    responseSender    service.ResponseSender
}
```

#### ValidationService (service/validation_service.go)

Orchestrates the entire validation process:

```go
type ValidationService struct {
    logger          *slog.Logger
    legacyStrategy  ValidationStrategy
    eflintStrategy  ValidationStrategy   // may be nil if unavailable
    configRepo      repository.ProviderConfigRepository
    tokenGenerator  AuthTokenGenerator
}
```

#### Enforcer (policyenforcer/enforcer.go)

The core enforcement component for the HTTP API, wrapping a `Reasoner`:

```go
type Enforcer struct {
    reasoner reasoner.Reasoner
    logger   *slog.Logger
}
```

### 5.3 Strategy Pattern — Validation Strategies

The Policy Enforcer uses the **Strategy pattern** to support multiple validation backends. Each data provider can be validated using a different strategy, resolved at runtime.

#### Interface

```go
type ValidationStrategy interface {
    Validate(steward, userName string) *ValidationResult
    Name() string
}
```

#### ValidationResult

```go
type ValidationResult struct {
    Steward            string
    IsValid            bool
    InvalidReason      string
    MatchedArchetypes  []string
    MatchedComputeProvs []string
    UserRelation       *api.Relation
}
```

#### Strategy Implementations

| Strategy | File | Data Source | Description |
|----------|------|-------------|-------------|
| `LegacyValidationStrategy` | `service/legacy_validation_strategy.go` | etcd JSON agreements | Validates against traditional JSON agreement documents |
| `EflintValidationStrategy` | `service/eflint_validation_strategy.go` | `EflintReasoner` (pool-based) | Delegates to the Reasoner for eFLINT policy validation |

#### Strategy Resolution

The strategy for each provider is resolved dynamically by `resolveStrategy()`:

```go
func (vs *ValidationService) resolveStrategy(provider string) ValidationStrategy {
    config, found, err := vs.configRepo.GetProviderConfig(provider)
    // etcd key: /policyEnforcer/configs/{provider}

    if found && config.ValidationStrategy == "eflint" && vs.eflintStrategy != nil {
        return vs.eflintStrategy
    }
    return vs.legacyStrategy  // default fallback
}
```

This means that in a single request with multiple data providers (e.g., `["VU", "UVA", "RUG"]`), different strategies can be used:

```
Request: dataProviders = ["VU", "UVA", "RUG"]

  VU  → config says "eflint"  → EflintValidationStrategy
  UVA → config says "eflint"  → EflintValidationStrategy
  RUG → config says "legacy"  → LegacyValidationStrategy

All three validated concurrently using goroutines.
```

### 5.4 Reasoner Abstraction

The `Reasoner` interface provides a **reasoner-agnostic** abstraction used by both the HTTP API and the `EflintValidationStrategy` (RabbitMQ flow). This allows the policy enforcement endpoints to work with any reasoner backend.

```go
type Reasoner interface {
    GetAllowedRequestTypes(ctx, org, requester string) ([]string, error)
    GetAllowedDataSets(ctx, org, requester string) ([]string, error)
    GetAllowedArchetypes(ctx, org, requester string) ([]string, error)
    GetAllowedComputeProviders(ctx, org, requester string) ([]string, error)
    GetAllAllowedClauses(ctx, org, requester string) (*AllAllowedClauses, error)
    IsRequestAllowed(ctx, params RequestParams) (*RequestValidationResult, error)
    IsRunning() bool
    Name() string
}
```

> **Note:** The individual `GetAllowed*` methods remain on the Reasoner interface for flexibility,
> but the HTTP API only exposes `GetAllAllowedClauses` (which fetches all clause types in a single call)
> and `IsRequestAllowed` (for validation).

The `EflintReasoner` implements the `Reasoner` interface. It is backed by the **eFLINT instance pool** and the **eFLINT model repository**. Each method follows the pool lifecycle:
1. **Acquire** an idle instance from the pool
2. **Load** the organization's eFLINT model from etcd via `SendPhrases`
3. **Execute** the query (facts, enabled, etc.)
4. **Release** the instance asynchronously

Both the HTTP path (`Enforcer` → `EflintReasoner`) and the RabbitMQ path (`EflintValidationStrategy` → `EflintReasoner`) share the same reasoner instance, ensuring consistent behaviour.

Optional capability interface:

```go
type AvailabilityProvider interface {
    GetAvailableArchetypes(ctx, org string) ([]string, error)
    GetAvailableComputeProviders(ctx, org string) ([]string, error)
}
```

The `EflintReasoner` also implements `AvailabilityProvider`.

### 5.5 Repository Pattern

Repositories abstract the etcd data access layer behind clean interfaces:

| Repository | Interface | etcd Key Pattern | Used By |
|-----------|-----------|-----------------|---------|
| `AgreementRepository` | `GetAgreement(steward) → Agreement` | `/policyEnforcer/agreements/{steward}` | `LegacyValidationStrategy` |
| `EflintModelRepository` | `GetEflintModel(name) → string` | `/policyEnforcer/eflintModels/{name}` | `EflintReasoner` |
| `ProviderConfigRepository` | `GetProviderConfig(provider) → ProviderValidationConfig` | `/policyEnforcer/configs/{provider}` | `ValidationService` |
| `EflintStateRepository` | `GetEflintState() / SaveEflintState()` | `/policyEnforcer/eflint-states/{provider}` | State management (POC) |

All repositories have an `Etcd*Repository` implementation that reads from / writes to etcd.

---

## 6. eFLINT Instance Pool

### 6.1 Architecture

The eFLINT instance pool manages a set of pre-started eFLINT server processes. Each pool instance is an independent eFLINT server running on a unique TCP port, bootstrapped with an empty model. When a validation request arrives, an idle instance is acquired, loaded with the relevant organisation's policy model, queried, and then reverted back to its empty state before being returned to the pool.

```
┌──────────────────────────────────────────────────┐
│                  InstancePool                      │
│                                                    │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐        │
│  │ Instance  │  │ Instance  │  │ Instance  │ ...   │
│  │ (idle)    │  │ (in_use)  │  │ (idle)    │        │
│  │ port:9001 │  │ port:9002 │  │ port:9003 │        │
│  │           │  │           │  │           │        │
│  │ Manager   │  │ Manager   │  │ Manager   │        │
│  │ StateMgr  │  │ StateMgr  │  │ StateMgr  │        │
│  └──────────┘  └──────────┘  └──────────┘        │
│                                                    │
│  Health Monitor (background goroutine)             │
│  ├── Checks health every 10s                       │
│  ├── Replaces unhealthy instances                  │
│  └── Enforces target pool size                     │
└──────────────────────────────────────────────────┘
```

### 6.2 Pool Lifecycle

```
Startup                     Validation Request              After Validation
───────                     ──────────────────              ────────────────
1. Create pool with         1. Acquire() → get idle         1. Revert to empty
   target size (3)             instance                        state (node 1)
2. Start N eFLINT           2. Mark as "in_use"             2. Verify empty
   server processes         3. Load org model                  state
3. Bootstrap each with         (SendPhrases)                3. Mark as "idle"
   empty.eflint             4. Query facts                  4. Return to pool
4. All start as "idle"      5. Parse + filter results          (async goroutine)
5. Start health monitor
```

### 6.3 Instance States

Each pool instance (`PoolEntry`) can be in one of three states:

| State | Description |
|-------|-------------|
| `idle` | Available for acquisition. Instance has an empty model. |
| `in_use` | Currently acquired by a validation goroutine. |
| `unhealthy` | Process crashed or health check failed. Will be replaced. |

### 6.4 Health Monitoring

A background goroutine runs every `HealthCheckInterval` (default: 10 seconds):

1. **Check health**: Verifies each instance's process is alive
2. **Replace unhealthy**: Stops and replaces any unhealthy instances
3. **Enforce target size**: Spins up new instances if below target, stops excess idle instances if above
4. **Log statistics**: Reports pool utilisation metrics

### 6.5 State Management

Each `PoolEntry` has a `StateManager` that handles:

- **Revert**: Sends `revert` command to node 1 (initial empty state)
- **Verify empty state**: Checks that `target_contents` is empty after revert
- **Export/Import**: Serialises/deserialises the eFLINT execution graph (for the POC state API)
- **Checkpoints**: Named snapshots of eFLINT state that can be restored

The state manager also handles **eFLINT server bugs** by transforming the execution graph JSON during import (renaming `"program"` → `"label"`, stripping `"Type extension"` lines).

### 6.6 eFLINT Commands

The Policy Enforcer communicates with eFLINT server instances over TCP using JSON commands:

| Command | Purpose | Example |
|---------|---------|---------|
| `facts` | Get all current facts | `{"command": "facts"}` |
| `phrases` | Load a full eFLINT specification | `{"command": "phrases", "text": "..."}` |
| `status` | Get server status and execution graph | `{"command": "status"}` |
| `create-export` | Export execution graph for persistence | `{"command": "create-export"}` |
| `load-export` | Import a previously exported graph | `{"command": "load-export", "graph": {...}}` |
| `revert` | Revert to a specific execution node | `{"command": "revert", "value": 1}` |
| `enabled` | Check if an action is enabled | `{"command": "enabled", "value": {...}}` |

---

## 7. Configuration

Configuration is managed via **Go build tags** — there are no environment variables. Use `go build -tags local` for local development or the default build for production.

### 7.1 Local Configuration

File: `config_local.go` (build tag: `local`)

| Setting | Value |
|---------|-------|
| Service Name | `"policyEnforcer"` |
| etcd Endpoints | `http://localhost:30005` |
| gRPC Address (sidecar) | `localhost:50051` |
| HTTP Port | `:8082` |
| API Version Prefix | `/api/v1` |
| eFLINT Server Path | `"eflint-server"` |
| eFLINT Model Path | `eflint/empty.eflint` (relative) |
| eFLINT State Directory | `eflint-states` (relative) |
| eFLINT Connection Timeout | 60 seconds |
| eFLINT Startup Delay | 3 seconds |
| eFLINT Port Range | 1025–65535 |
| Auto Start eFLINT | `true` |
| Pool Size | 3 |
| Health Check Interval | 10 seconds |
| Acquire Timeout | 30 seconds |

### 7.2 Production Configuration

File: `config_prod.go` (build tag: `!local`)

| Setting | Value |
|---------|-------|
| Service Name | `"policyEnforcer"` |
| etcd Endpoints | Kubernetes cluster endpoints (3 nodes) |
| gRPC Address (sidecar) | `localhost:50051` |
| HTTP Port | `:8080` |
| API Version Prefix | `/api/v1` |
| eFLINT Server Path | `"eflint-server"` |
| eFLINT Model Path | `""` (uses embedded `empty.eflint`) |
| eFLINT State Directory | `/app/eflint-states` |
| eFLINT Connection Timeout | 60 seconds |
| eFLINT Startup Delay | 3 seconds |
| eFLINT Port Range | 1025–65535 |
| Auto Start eFLINT | `true` |
| Pool Size | 3 |
| Health Check Interval | 10 seconds |
| Acquire Timeout | 30 seconds |

### 7.3 etcd Key Paths

| Path | Purpose | Data Format | Used By |
|------|---------|-------------|---------|
| `/policyEnforcer/agreements/{steward}` | Legacy JSON agreement | JSON (`api.Agreement`) | `LegacyValidationStrategy` |
| `/policyEnforcer/eflintModels/{provider}` | eFLINT policy model text | Plain text (`.eflint`) | `EflintReasoner` |
| `/policyEnforcer/configs/{provider}` | Provider validation config | JSON (`api.ProviderValidationConfig`) | `ValidationService` |
| `/policyEnforcer/eflint-states/{provider}` | Saved eFLINT states | JSON | State management (POC) |
| `/agents/online/{name}` | Online agent status | JSON | Orchestrator (not Policy Enforcer) |
| `/archetypes/{name}` | Archetype configurations | JSON | Orchestrator (not Policy Enforcer) |

### 7.4 Provider Validation Configs

Each data provider's validation strategy is configured in etcd at `/policyEnforcer/configs/{provider}`:

```json
{
  "name": "VU",
  "validationStrategy": "eflint",
  "agreementLocation": "/app/eflint-models/VU.eflint"
}
```

Example configuration set (from `configuration/etcd_launch_files/provider_configs.json`):

| Provider | Strategy | Agreement Location |
|----------|----------|-------------------|
| VU | `eflint` | `/app/eflint-models/VU.eflint` |
| UVA | `eflint` | `/app/eflint-models/UVA.eflint` |
| RUG | `legacy` | `/policyEnforcer/agreements/RUG` |

---

## 8. Key Data Structures (Protobuf)

### RequestApproval (API Gateway → Policy Enforcer)

```protobuf
message RequestApproval {
  string type = 1;                        // "requestApproval"
  User user = 2;                          // {id, user_name}
  repeated string data_providers = 3;     // ["VU", "UVA", "RUG"]
  string destination_queue = 4;           // "policyEnforcer-in"
  map<string, bool> options = 5;
}
```

### ValidationResponse (Policy Enforcer → Orchestrator)

```protobuf
message ValidationResponse {
  string type = 1;                                       // "validationResponse"
  string request_type = 2;                               // e.g., "sqlDataRequest"
  map<string, DataProvider> valid_dataproviders = 3;     // validated providers
  repeated string invalid_dataproviders = 4;             // rejected providers
  Auth auth = 5;                                         // auth tokens
  User user = 6;
  bool request_approved = 7;
  UserArchetypes valid_archetypes = 8;
  map<string, bool> options = 9;
}
```

### PolicyUpdate (Orchestrator ↔ Policy Enforcer)

```protobuf
message PolicyUpdate {
  string type = 1;                            // "policyUpdate"
  User user = 2;
  repeated string data_providers = 3;
  RequestMetadata request_metadata = 4;
  ValidationResponse validation_response = 5; // populated on return
}
```

### Supporting Types

```protobuf
message User {
  string id = 1;
  string user_name = 2;
}

message Auth {
  string access_token = 1;
  string refresh_token = 2;
}

message DataProvider {
  repeated string archetypes = 1;
  repeated string compute_providers = 2;
}

message UserArchetypes {
  string user_name = 1;
  map<string, UserAllowedArchetypes> archetypes = 2;  // key = provider
}

message RequestMetadata {
  string correlation_id = 1;
  string destination_queue = 2;
  string job_name = 3;
  string return_address = 4;
  string job_id = 5;
  map<string, bytes> traces = 6;
}
```

---

## 9. Deployment

### Kubernetes Deployment

The Policy Enforcer is deployed via the Helm chart at `charts/orchestrator/templates/policyEnforcer.yaml`.

**Key deployment details:**

| Setting | Value |
|---------|-------|
| Replicas | 1 |
| Container Port | 8080 |
| Image | `{dockerArtifactAccount}/policy-enforcer:{branchNameTag}` |
| Readiness Probe | `GET /api/v1/health` (initial: 5s, period: 10s) |
| Liveness Probe | `GET /api/v1/health` (initial: 15s, period: 20s) |

**Volumes:**

| Volume | Type | Mount Path | Purpose |
|--------|------|-----------|---------|
| `eflint-states` | `emptyDir` | `/app/eflint-states` | Runtime eFLINT state files |
| `eflint-models` | PVC (`etcd-pvc`) | `/app/eflint-models` | eFLINT model files (shared with etcd) |

**Sidecar:**

The Policy Enforcer runs alongside a **sidecar** container that handles RabbitMQ communication via gRPC:

- Image: `{dockerArtifactAccount}/sidecar:{branchNameTag}`
- Connects to RabbitMQ using credentials from the `rabbit` Kubernetes secret
- Exposes gRPC on `localhost:50051` (pod-internal)

**Ingress:**

- Host: `policy-enforcer.orchestrator.svc.cluster.local`
- Path: `/api/v1` (Prefix)
- Ingress class: `nginx`

---

## 10. Diagrams

For visual representations of the request approval flow (PlantUML activity diagrams, sequence diagrams, component diagrams, and architecture overview), see:

- **PlantUML source + embedded diagrams:** [`docs/diagrams/request_approval_diagrams.md`](../diagrams/request_approval_diagrams.md) — covers the full dual-strategy validation flow (Legacy + eFLINT), eFLINT pool lifecycle, HTTP API flow, software architecture, and deployment.
- **C4 model diagrams:** [`docs/diagrams/request_approval_c4.md`](../diagrams/request_approval_c4.md) — C4 Levels 1–4 including the updated Policy Enforcer component diagram with strategy resolution, pool, reasoner, and repository layers.
- **Legacy (old) diagrams:** [`docs/diagrams/old_request_approval_diagrams.md`](../diagrams/old_request_approval_diagrams.md) and [`docs/diagrams/old_request_approval_c4.md`](../diagrams/old_request_approval_c4.md) — diagrams from the previous codebase (before dual-strategy validation).
- **Legacy flow documentation:** [`docs/development_guide/legacy_request_approval_flow.md`](./legacy_request_approval_flow.md)
