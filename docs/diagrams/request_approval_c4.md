# C4 Diagrams — Request Approval Flow

This document describes the current DYNAMOS Request Approval flow using the
[C4 model](https://c4model.com/) (System Context, Container, Component, Code).
Each level is embedded as a PlantUML block that uses the
[C4-PlantUML](https://github.com/plantuml-stdlib/C4-PlantUML) standard library.

The key difference from the [legacy C4 diagrams](./old_request_approval_c4.md)
is the **dual-strategy validation** in the Policy Enforcer: per-provider strategy
resolution (Legacy JSON vs eFLINT), an eFLINT instance pool for concurrent
stateless validation, a reasoner abstraction for the HTTP API, and a repository
pattern for etcd access.

> **See also:**
> - [PlantUML activity, sequence & component diagrams](./request_approval_diagrams.md)
> - [Policy Enforcer technical documentation](../development_guide/policy_enforcer.md)
> - [Legacy C4 diagrams](./old_request_approval_c4.md)

---

## Level 1 — System Context

Shows DYNAMOS as a single system box in the context of its users and any
external systems it depends on.

```plantuml
@startuml C4_Level1_SystemContext
!include <C4/C4_Context>

title DYNAMOS — System Context Diagram (Level 1)

Person(analyst, "Data Analyst", "Submits data requests and receives aggregated results.")

System(dynamos, "DYNAMOS Platform", "Validates data-access requests against organisational policies (via legacy JSON agreements or formal eFLINT models), orchestrates approved jobs, and dispatches work to data-provider agents.")

System_Ext(agents, "Data-Provider Agents", "Autonomous agents (VU, UVA, SURF, …) that expose data sets and compute capabilities on behalf of their organisations.")

Rel(analyst, dynamos, "Sends requestApproval & receives results", "HTTP / JSON")
Rel(analyst, dynamos, "Queries allowed clauses & validates requests", "HTTP / JSON")
Rel(dynamos, agents, "Dispatches data requests to authorised agents", "HTTP POST")

SHOW_LEGEND()
@enduml
```

---

## Level 2 — Container

Zooms into the DYNAMOS system to reveal its main runtime containers and the
technologies that connect them. The Policy Enforcer now includes an eFLINT
instance pool and an HTTP API alongside its RabbitMQ message processing.

```plantuml
@startuml C4_Level2_Container
!include <C4/C4_Container>

title DYNAMOS — Container Diagram (Level 2)

Person(analyst, "Data Analyst", "Submits data requests.")

System_Boundary(dynamos, "DYNAMOS Platform") {

    Container(api, "API Gateway", "Go", "Accepts HTTP requests, publishes to RabbitMQ, waits for approval, then forwards data requests to agents.")

    Container(pe, "Policy Enforcer", "Go", "Validates requests using dual-strategy validation (Legacy JSON or eFLINT). Manages an eFLINT instance pool. Exposes HTTP API for policy queries.")

    Container(orch, "Orchestrator", "Go", "Checks agent availability, selects an archetype, starts composition requests, and builds the final approval response.")

    ContainerQueue(rabbit, "RabbitMQ", "AMQP", "Message broker with per-service queues: policyEnforcer-in, orchestrator-in, api-gateway-in, {agent}-in.")

    ContainerDb(etcd, "etcd", "Key-Value Store", "Stores agreements, eFLINT models, provider configs, agent-online status, and archetype configurations.")

    Container(eflint, "eFLINT Server Instances", "Haskell (TCP)", "Stateless eFLINT reasoner processes managed by the Policy Enforcer pool. Each runs on a unique TCP port.")
}

System_Ext(agents, "Data-Provider Agents", "VU, UVA, SURF — expose data/compute endpoints.")

Rel(analyst, api, "HTTP POST /api/v1/requestApproval", "JSON")
Rel(analyst, pe, "HTTP GET/POST /api/v1/policy-enforcer/*", "JSON (optional direct queries)")

Rel(api, rabbit, "Publish requestApproval", "policyEnforcer-in")
Rel(rabbit, pe, "Consume requestApproval", "AMQP")

Rel(pe, etcd, "GET /policyEnforcer/configs/{prov}\nGET /policyEnforcer/agreements/{steward}\nGET /policyEnforcer/eflintModels/{prov}", "HTTP / gRPC")
Rel(pe, eflint, "TCP commands: phrases, facts, revert, status", "JSON over TCP")
Rel(pe, rabbit, "Publish validationResponse", "orchestrator-in")

Rel(rabbit, orch, "Consume validationResponse", "AMQP")
Rel(orch, etcd, "GET /agents/online/{name}\nGET /archetypes/{name}", "HTTP / gRPC")
Rel(orch, rabbit, "Publish compositionRequest", "{agent}-in")
Rel(orch, rabbit, "Publish requestApprovalResponse", "api-gateway-in")

Rel(rabbit, api, "Consume requestApprovalResponse", "AMQP")
Rel(api, agents, "HTTP POST /agent/v1/{type}/{target}", "JSON")

Rel_Back(agents, api, "Response data")

SHOW_LEGEND()
@enduml
```

---

## Level 3 — Component

Decomposes each container into its key internal components and shows how they
collaborate during the request approval flow.

### 3a — API Gateway Components

```plantuml
@startuml C4_Level3_APIGateway
!include <C4/C4_Component>

title API Gateway — Component Diagram (Level 3)

Container_Boundary(api, "API Gateway") {

    Component(handler, "requestHandler", "Go func", "Parses the incoming HTTP POST, creates a RequestApproval protobuf, stores a response channel in requestApprovalMap, and publishes the message to RabbitMQ.")

    Component(approvalMap, "requestApprovalMap", "Go map[string]chan", "In-memory map that correlates a user ID with a channel waiting for the approval response.")

    Component(incomingHandler, "handleIncomingMessages", "Go func", "RabbitMQ consumer that routes incoming messages by type. On 'requestApprovalResponse' it looks up the channel in the map and pushes the response.")

    Component(sendData, "sendDataToAuthProviders", "Go func", "Iterates over authorised providers, adds request metadata and trace context, then sends parallel HTTP POST requests to each agent.")
}

ContainerQueue(rabbit, "RabbitMQ", "AMQP")
ContainerDb(etcd, "etcd", "KV Store")
System_Ext(agents, "Data-Provider Agents", "VU, UVA, SURF")
Person(analyst, "Data Analyst", "")

Rel(analyst, handler, "HTTP POST /api/v1/requestApproval")
Rel(handler, approvalMap, "Store response channel")
Rel(handler, rabbit, "Publish requestApproval → policyEnforcer-in")

Rel(rabbit, incomingHandler, "Consume requestApprovalResponse")
Rel(incomingHandler, approvalMap, "Lookup & push response")

Rel(approvalMap, sendData, "Unblocks with approval")
Rel(sendData, agents, "HTTP POST /agent/v1/{type}/{target}")

SHOW_LEGEND()
@enduml
```

### 3b — Policy Enforcer Components

```plantuml
@startuml C4_Level3_PolicyEnforcer
!include <C4/C4_Component>

title Policy Enforcer — Component Diagram (Level 3)

Container_Boundary(pe, "Policy Enforcer") {

    Component(router, "handleIncomingMessages", "Go func [consume.go]", "RabbitMQ consumer that routes messages by type. Dispatches 'requestApproval' to handleRequestApproval and 'policyUpdate' to handlePolicyUpdate.")

    Component(valSvc, "ValidationService", "Go struct [service/validation_service.go]", "Orchestrates concurrent per-provider validation. Resolves strategy per provider, launches goroutines, collects results, generates auth tokens.")

    Component(resolver, "resolveStrategy", "Go func", "Queries etcd /policyEnforcer/configs/{provider} to determine whether to use the Legacy or eFLINT validation strategy.")

    Component(legacyStrat, "LegacyValidationStrategy", "Go struct [service/legacy_validation_strategy.go]", "Validates against JSON agreement documents in etcd. Checks user relations, matches archetypes and compute providers.")

    Component(eflintStrat, "EflintValidationStrategy", "Go struct [service/eflint_validation_strategy.go]", "Delegates to the EflintReasoner for eFLINT policy validation. Maps Reasoner results to ValidationResult.")

    Component(pool, "InstancePool", "Go struct [eflint/pool.go]", "Manages pre-started eFLINT server processes. Provides Acquire/Release with health monitoring and dynamic resizing.")

    Component(reasoner, "EflintReasoner", "Go struct [reasoner/eflint_reasoner.go]", "Pool-aware Reasoner implementation. Acquires pool instances, loads models from etcd, queries eFLINT, releases instances. Used by both the HTTP API and the EflintValidationStrategy.")

    Component(enforcer, "Enforcer", "Go struct [policyenforcer/enforcer.go]", "Core enforcement component for HTTP API. Wraps a Reasoner and provides high-level policy query methods.")

    Component(httpHandler, "PolicyEnforcerHTTPHandler", "Go struct [policyenforcerhttp/]", "HTTP handlers for /api/v1/policy-enforcer/* endpoints. Delegates to Enforcer.")

    Component(instAPI, "eFLINT Instance & Pool API", "Go [eflint/instance_api.go]", "HTTP handlers for /api/v1/eflint/* endpoints. Instance lifecycle and pool management.")

    Component(stateAPI, "eFLINT State API", "Go [eflint/state_api.go]", "HTTP handlers for /api/v1/eflint/state/* endpoints. Checkpoint/restore (POC).")

    Component(auth, "Auth Token Generator", "Go interface [service/]", "Creates a signed authentication token included in the ValidationResponse.")

    Component(configRepo, "ProviderConfigRepository", "Go interface [repository/]", "Reads provider validation configs from etcd /policyEnforcer/configs/{provider}.")

    Component(agreementRepo, "AgreementRepository", "Go interface [repository/]", "Reads JSON agreements from etcd /policyEnforcer/agreements/{steward}.")

    Component(modelRepo, "EflintModelRepository", "Go interface [repository/]", "Reads eFLINT model text from etcd /policyEnforcer/eflintModels/{provider}.")
}

ContainerQueue(rabbit, "RabbitMQ", "AMQP")
ContainerDb(etcd, "etcd", "KV Store")
Container(eflintServers, "eFLINT Server Instances", "TCP processes")
Person(analyst, "Data Analyst / Dashboard", "")

' Message flow
Rel(rabbit, router, "Consume requestApproval / policyUpdate\nfrom policyEnforcer-in")
Rel(router, valSvc, "Dispatch request")
Rel(valSvc, resolver, "Resolve strategy per provider")
Rel(resolver, configRepo, "Get provider config")
Rel(configRepo, etcd, "GET /policyEnforcer/configs/{prov}")

Rel(valSvc, legacyStrat, "Legacy path")
Rel(legacyStrat, agreementRepo, "Get agreement")
Rel(agreementRepo, etcd, "GET /policyEnforcer/agreements/{steward}")

Rel(valSvc, eflintStrat, "eFLINT path")
Rel(eflintStrat, reasoner, "Delegates to Reasoner")

Rel(reasoner, pool, "Acquire / Release instance")
Rel(reasoner, modelRepo, "Get eFLINT model")
Rel(modelRepo, etcd, "GET /policyEnforcer/eflintModels/{prov}")
Rel(pool, eflintServers, "TCP: phrases, facts, revert, status")

Rel(valSvc, auth, "Generate auth token")
Rel(valSvc, rabbit, "Publish validationResponse → orchestrator-in")

' HTTP API flow
Rel(analyst, httpHandler, "HTTP GET/POST /api/v1/policy-enforcer/*")
Rel(httpHandler, enforcer, "Delegates")
Rel(enforcer, reasoner, "Delegates")

Rel(analyst, instAPI, "HTTP /api/v1/eflint/*")
Rel(instAPI, pool, "Pool management")
Rel(instAPI, eflintServers, "Instance lifecycle")

Rel(analyst, stateAPI, "HTTP /api/v1/eflint/state/*")
Rel(stateAPI, eflintServers, "State operations")

SHOW_LEGEND()
@enduml
```

### 3c — Orchestrator Components

```plantuml
@startuml C4_Level3_Orchestrator
!include <C4/C4_Component>

title Orchestrator — Component Diagram (Level 3)

Container_Boundary(orch, "Orchestrator") {

    Component(router, "handleIncomingMessages", "Go func", "RabbitMQ consumer that routes messages by type. Dispatches 'validationResponse' to handleRequestApproval.")

    Component(handleRA, "handleRequestApproval", "Go func", "Entry point: calls getAuthorizedProviders, then startCompositionRequest, and finally builds & sends the RequestApprovalResponse.")

    Component(authProv, "getAuthorizedProviders", "Go func", "For each ValidDataprovider, queries etcd /agents/online/{provider}. Returns only the providers that are currently online.")

    Component(compose, "startCompositionRequest", "Go func", "Selects the best archetype (chooseArchetype), retrieves its config from etcd, generates a job name, and sends CompositionRequest messages to each authorised provider/agent queue.")

    Component(chooseArch, "chooseArchetype", "Go func", "Evaluates valid archetypes and picks the optimal one (computeToData vs dataThroughTtp).")
}

ContainerQueue(rabbit, "RabbitMQ", "AMQP")
ContainerDb(etcd, "etcd", "KV Store")

Rel(rabbit, router, "Consume validationResponse from orchestrator-in")
Rel(router, handleRA, "Dispatch validationResponse")
Rel(handleRA, authProv, "Get authorized providers")
Rel(authProv, etcd, "GET /agents/online/{provider}")
Rel(handleRA, compose, "Start composition")
Rel(compose, chooseArch, "Select archetype")
Rel(compose, etcd, "GET /archetypes/{archetype}")
Rel(compose, rabbit, "Publish compositionRequest → {agent}-in")
Rel(handleRA, rabbit, "Publish requestApprovalResponse → api-gateway-in")

SHOW_LEGEND()
@enduml
```

---

## Level 4 — Code

Shows the key data structures (protobuf messages) that flow between the
containers, the etcd key-space, and the internal Policy Enforcer types. This
level is derived from the current codebase.

### 4a — Protobuf Message Structures

```plantuml
@startuml C4_Level4_Code
!theme cerulean

title Request Approval — Key Data Structures (Level 4)

class RequestApproval <<protobuf>> {
  +type : string            = "requestApproval"
  +user : User
  +data_providers : string[]
  +destination_queue : string = "policyEnforcer-in"
  +options : map<string, bool>
}

class ValidationResponse <<protobuf>> {
  +type : string                    = "validationResponse"
  +request_type : string
  +valid_dataproviders : map<string, DataProvider>
  +invalid_dataproviders : string[]
  +auth : Auth
  +user : User
  +request_approved : bool
  +valid_archetypes : UserArchetypes
  +options : map<string, bool>
}

class RequestApprovalResponse <<protobuf>> {
  +type : string                 = "requestApprovalResponse"
  +user : User
  +auth : Auth
  +authorized_providers : map<string, string>
  +job_id : string
  +error : string
  +request_metadata : RequestMetadata
}

class CompositionRequest <<protobuf>> {
  +archetype_id : string
  +request_type : string
  +role : string      // "all" | "dataProvider" | "computeProvider"
  +user : User
  +data_providers : string[]
  +destination_queue : string
  +job_name : string
  +local_job_name : string
}

class PolicyUpdate <<protobuf>> {
  +type : string = "policyUpdate"
  +user : User
  +data_providers : string[]
  +request_metadata : RequestMetadata
  +validation_response : ValidationResponse
}

class User <<protobuf>> {
  +id : string
  +user_name : string
}

class Auth <<protobuf>> {
  +access_token : string
  +refresh_token : string
}

class DataProvider <<protobuf>> {
  +archetypes : string[]
  +compute_providers : string[]
}

class UserArchetypes <<protobuf>> {
  +user_name : string
  +archetypes : map<string, UserAllowedArchetypes>
}

class UserAllowedArchetypes <<protobuf>> {
  +archetypes : string[]
}

class RequestMetadata <<protobuf>> {
  +correlation_id : string
  +destination_queue : string = "api-gateway-in"
  +job_name : string
  +return_address : string
  +job_id : string
  +traces : map<string, bytes>
}

RequestApproval --> User
RequestApproval ..> "policyEnforcer-in" : published to

ValidationResponse --> User
ValidationResponse --> Auth
ValidationResponse --> DataProvider : valid_dataproviders
ValidationResponse --> UserArchetypes
ValidationResponse ..> "orchestrator-in" : published to (hardcoded)

RequestApprovalResponse --> User
RequestApprovalResponse --> Auth
RequestApprovalResponse --> RequestMetadata
RequestApprovalResponse ..> "api-gateway-in" : published to

CompositionRequest --> User
CompositionRequest ..> "{agent}-in" : published to

PolicyUpdate --> User
PolicyUpdate --> RequestMetadata
PolicyUpdate --> ValidationResponse : embeds on return
PolicyUpdate ..> "policyEnforcer-in / orchestrator-in" : consumed from / published to

UserArchetypes --> "0..*" UserAllowedArchetypes : archetypes\n(map key = provider)

@enduml
```

### 4b — Policy Enforcer Internal Types

```plantuml
@startuml C4_Level4_PE_Types
!theme cerulean

title Policy Enforcer — Internal Types (Level 4)

package "Validation Pipeline" {

    interface ValidationStrategy <<interface>> {
        +Validate(steward, userName string) *ValidationResult
        +Name() string
    }

    class ValidationResult {
        +Steward : string
        +IsValid : bool
        +InvalidReason : string
        +MatchedArchetypes : []string
        +MatchedComputeProvs : []string
        +UserRelation : *Relation
    }

    class ProviderValidationConfig {
        +Name : string
        +ValidationStrategy : string   // "legacy" | "eflint"
        +AgreementLocation : string
    }
}

package "Reasoner Layer" {

    interface Reasoner <<interface>> {
        +GetAllAllowedClauses(ctx, org, req) (*AllAllowedClauses, error)
        +IsRequestAllowed(ctx, params) (*RequestValidationResult, error)
        +IsRunning() bool
        +Name() string
    }

    class AllAllowedClauses {
        +RequestTypes : []string
        +DataSets : []string
        +Archetypes : []string
        +ComputeProviders : []string
    }

    class RequestValidationResult {
        +Allowed : bool
        +Reason : string
    }

    class ValidateRequestParams {
        +Organization : string
        +Requester : string
        +RequestType : string
        +DataSet : string
        +Archetype : string
        +ComputeProvider : string
    }
}

package "eFLINT Pool" {

    class PoolEntry {
        +ID : string
        +Manager : *Manager
        +StateManager : *StateManager
        -state : string
    }

    class PoolStatistics {
        +TargetSize : int
        +Total : int
        +Idle : int
        +InUse : int
        +Unhealthy : int
    }

    class PoolConfig {
        +TargetSize : int
        +HealthCheckInterval : time.Duration
        +AcquireTimeout : time.Duration
        +ManagerConfig : ManagerConfig
    }
}

package "etcd Data" {

    class Agreement <<etcd JSON>> {
        +Name : string
        +Relations : map<string, Relation>
        +Archetypes : []string
        +ComputeProviders : []string
    }

    class Relation <<etcd JSON>> {
        +Id : string
        +RequestTypes : []string
        +DataSets : []string
        +AllowedArchetypes : []string
        +AllowedComputeProviders : []string
    }

    Agreement --> "0..*" Relation : relations\n(map key = userId)
}

ValidationStrategy --> ValidationResult : returns
ProviderValidationConfig ..> ValidationStrategy : selects

Reasoner --> AllAllowedClauses : returns
Reasoner --> RequestValidationResult : returns
Reasoner --> ValidateRequestParams : accepts

@enduml
```

### 4c — etcd Key Space

```plantuml
@startuml C4_Level4_etcd
!theme cerulean

title Request Approval — etcd Key Space (Level 4)

package "etcd Key-Value Store" {

    object "/policyEnforcer/agreements/{steward}" as agreements {
        name : string
        relations : map<userId, Relation>
        archetypes : string[]
        computeProviders : string[]
    }

    object "/policyEnforcer/eflintModels/{provider}" as eflintModels {
        <raw eFLINT specification text>
    }

    object "/policyEnforcer/configs/{provider}" as configs {
        name : string
        validationStrategy : string  // "legacy" | "eflint"
        agreementLocation : string
    }

    object "/policyEnforcer/eflint-states/{provider}" as eflintStates {
        graph : JSON (execution graph)
        checkpoint_name : string
    }

    object "/agents/online/{name}" as agentOnline {
        name : string
        dns : string
        routingKey : string
    }

    object "/archetypes/{name}" as archetypes {
        name : string
        type : string  // "computeToData" | "dataThroughTtp"
        config : JSON
    }
}

note right of agreements
  Read by **LegacyValidationStrategy**
  Legacy JSON agreement path
end note

note right of eflintModels
  Read by **EflintReasoner**
  Loaded into pool instances
  via SendPhrases command
end note

note right of configs
  Read by **ValidationService.resolveStrategy()**
  Determines which strategy to use
  per data provider
end note

note right of eflintStates
  Read/written by **EflintStateRepository**
  POC state persistence
end note

note right of agentOnline
  Read by **Orchestrator**
  in getAuthorizedProviders()
end note

note right of archetypes
  Read by **Orchestrator**
  in startCompositionRequest()
end note

@enduml
```

---

## How to Render

These diagrams use the [C4-PlantUML](https://github.com/plantuml-stdlib/C4-PlantUML) standard library
which is bundled with PlantUML since version 1.2021.1. You can render them with:

- **VS Code** — install the *PlantUML* extension (`jebbs.plantuml`) and preview the fenced blocks.
- **CLI** — `java -jar plantuml.jar request_approval_c4.md` (PlantUML renders `plantuml` fenced blocks inside Markdown).
- **Online** — paste each block into [plantuml.com](https://www.plantuml.com/plantuml/uml).
