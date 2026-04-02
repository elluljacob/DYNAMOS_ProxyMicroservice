# C4 Diagrams — Legacy Policy Update Flow

This document describes the legacy DYNAMOS Policy Update flow using the
[C4 model](https://c4model.com/) (System Context, Container, Component, Code).
Each level is embedded as a PlantUML block that uses the
[C4-PlantUML](https://github.com/plantuml-stdlib/C4-PlantUML) standard library.

> **Branch:** `legacy-policy-enforcer`
>
> See also: [Legacy Policy Update Flow](../development_guide/legacy_policy_update_flow.md)

---

## Level 1 — System Context

Shows DYNAMOS as a single system box in the context of its users and any
external systems it depends on.

```plantuml
@startuml C4_Level1_SystemContext
!include <C4/C4_Context>

title DYNAMOS — System Context Diagram (Level 1) — Policy Update

Person(admin, "External Client / Admin", "Updates organisational agreements via the Orchestrator HTTP API.")

System(dynamos, "DYNAMOS Platform", "Validates data-access agreements, orchestrates jobs, and dispatches work to data-provider agents. Policy updates propagate agreement changes to active jobs.")

System_Ext(agents, "Data-Provider Agents", "Autonomous agents (VU, UVA, SURF, …) that expose data sets and compute capabilities on behalf of their organisations.")

Rel(admin, dynamos, "HTTP PUT /api/v1/policyEnforcer/agreements", "Updated agreement JSON")
Rel(dynamos, agents, "May send updated CompositionRequests when archetype changes", "RabbitMQ")

SHOW_LEGEND()
@enduml
```

---

## Level 2 — Container

Zooms into the DYNAMOS system to reveal its main runtime containers and the
technologies that connect them during the policy update flow.

```plantuml
@startuml C4_Level2_Container
!include <C4/C4_Container>

title DYNAMOS — Container Diagram (Level 2) — Policy Update

Person(admin, "External Client / Admin", "Updates agreements.")

System_Boundary(dynamos, "DYNAMOS Platform") {

    Container(orch, "Orchestrator", "Go", "HTTP API for agreement management, job evaluation, archetype selection, and policy update coordination.")

    Container(pe, "Policy Enforcer", "Go", "Re-validates user agreements against data providers when policies change.")

    Container(orchSidecar, "Sidecar (Orchestrator)", "Go", "gRPC-to-RabbitMQ bridge for the orchestrator.")

    Container(peSidecar, "Sidecar (Policy Enforcer)", "Go", "gRPC-to-RabbitMQ bridge for the policy enforcer.")

    ContainerQueue(rabbit, "RabbitMQ", "AMQP", "Message broker with per-service queues: policyEnforcer-in, orchestrator-in, {agent}-in.")

    ContainerDb(etcd, "etcd", "Key-Value Store", "Stores agreements, agent-online status, job configurations, and archetype definitions.")
}

System_Ext(agents, "Data-Provider Agents", "VU, UVA, SURF — may receive new CompositionRequests.")

Rel(admin, orch, "HTTP PUT /api/v1/policyEnforcer/agreements", "JSON")

Rel(orch, etcd, "Read/Write agreements, jobs, agents, archetypes", "HTTP / gRPC")
Rel(orch, orchSidecar, "gRPC", "SendPolicyUpdate, SendCompositionRequest")

Rel(orchSidecar, rabbit, "Publish policyUpdate", "policyEnforcer-in")
Rel(rabbit, peSidecar, "Consume policyUpdate", "AMQP")

Rel(peSidecar, pe, "gRPC stream: SideCarMessage", "")
Rel(pe, etcd, "GET /policyEnforcer/agreements/{steward}", "HTTP / gRPC")
Rel(pe, peSidecar, "gRPC: SendPolicyUpdate (response)", "")

Rel(peSidecar, rabbit, "Publish policyUpdate (with ValidationResponse)", "orchestrator-in")
Rel(rabbit, orchSidecar, "Consume policyUpdate response", "AMQP")
Rel(orchSidecar, orch, "gRPC stream: SideCarMessage", "")

Rel(orch, rabbit, "Publish compositionRequest (if archetype changes)", "{agent}-in")

SHOW_LEGEND()
@enduml
```

---

## Level 3 — Component

Decomposes each container into its key internal components and shows how they
collaborate during the policy update flow.

### 3a — Orchestrator Components

```plantuml
@startuml C4_Level3_Orchestrator
!include <C4/C4_Component>

title Orchestrator — Component Diagram (Level 3) — Policy Update

Container_Boundary(orch, "Orchestrator") {

    Component(httpAPI, "HTTP API Layer", "api.go", "Exposes REST endpoints. Routes /policyEnforcer to agreementsHandler.")

    Component(agreementsHandler, "agreementsHandler", "api.go", "Handles PUT requests: saves agreement to etcd via GenericPutToEtcd, then triggers go checkJobs().")

    Component(checkJobs, "checkJobs", "manage_jobs.go", "Iterates relations in the updated agreement, finds active jobs in etcd, decides whether to delete or re-evaluate.")

    Component(deleteJobInfo, "deleteJobInfo", "manage_jobs.go", "Deletes all job entries across agents when a user loses all allowed archetypes.")

    Component(evalArchetype, "evaluateArchetypeInActiveJobs", "manage_jobs.go", "For each active job: builds a PolicyUpdate message, gathers agents via getJobAcrossAgents, stores context in policyUpdateMap, sends via sidecar.")

    Component(getJobAcross, "getJobAcrossAgents", "manage_jobs.go", "Scans all online agents (/agents/online/) and looks up each agent's entry for a specific job.")

    Component(policyUpdateMap, "policyUpdateMap", "main.go", "In-memory map[string]map[string]*CompositionRequest correlating correlation IDs to job context across agents.")

    Component(consumer, "handleIncomingMessages", "consume.go", "RabbitMQ consumer on orchestrator-in. Routes 'policyUpdate' messages: looks up context from policyUpdateMap, calls processPolicyUpdate.")

    Component(processPU, "processPolicyUpdate", "manage_jobs.go", "Processes the PolicyUpdate response: calls getAuthorizedProviders, chooseArchetype, then updates/deletes job entries in etcd. May send new CompositionRequest.")

    Component(getAuth, "getAuthorizedProviders", "get_authorized_providers.go", "For each ValidDataprovider, queries etcd /agents/online/{provider}. Returns only providers that are currently online.")

    Component(chooseArch, "chooseArchetype", "composition_request.go", "Evaluates valid archetypes and picks the optimal one based on options (aggregate flag) or weight. Falls back to first available.")

    Component(chooseTTP, "chooseThirdParty", "composition_request.go", "Finds the intersection of allowed compute providers across all valid data providers, picks the first one that is online.")
}

ContainerQueue(rabbit, "RabbitMQ", "AMQP")
ContainerDb(etcd, "etcd", "KV Store")

Rel(httpAPI, agreementsHandler, "Routes to")
Rel(agreementsHandler, etcd, "Saves agreement")
Rel(agreementsHandler, checkJobs, "go checkJobs()")
Rel(checkJobs, etcd, "Reads job names")
Rel(checkJobs, deleteJobInfo, "If no archetypes")
Rel(checkJobs, evalArchetype, "If archetypes exist")
Rel(evalArchetype, etcd, "Reads job info")
Rel(evalArchetype, getJobAcross, "Gathers agents")
Rel(getJobAcross, etcd, "Reads /agents/online/ and /agents/jobs/")
Rel(evalArchetype, policyUpdateMap, "Stores job context")
Rel(evalArchetype, rabbit, "SendPolicyUpdate (via sidecar)")

Rel(rabbit, consumer, "Delivers policyUpdate response")
Rel(consumer, policyUpdateMap, "Looks up job context by correlationId")
Rel(consumer, processPU, "Calls")
Rel(processPU, getAuth, "Gets online providers")
Rel(processPU, chooseArch, "Selects archetype")
Rel(processPU, chooseTTP, "If dataThroughTtp")
Rel(processPU, etcd, "Updates/deletes job entries")
Rel(processPU, rabbit, "SendCompositionRequest (if new TTP needed)")
Rel(getAuth, etcd, "Reads /agents/online/")
Rel(chooseArch, etcd, "Reads /archetypes/")
Rel(chooseTTP, etcd, "Reads /agents/online/")

SHOW_LEGEND()
@enduml
```

### 3b — Policy Enforcer Components

```plantuml
@startuml C4_Level3_PolicyEnforcer
!include <C4/C4_Component>

title Policy Enforcer — Component Diagram (Level 3) — Policy Update

Container_Boundary(pe, "Policy Enforcer") {

    Component(router, "handleIncomingMessages", "consume.go", "RabbitMQ consumer that routes messages by type. Dispatches 'policyUpdate' to checkPolicyUpdate.")

    Component(check, "checkPolicyUpdate", "policy_update.go", "Creates a ValidationResponse (type='policyUpdate'), calls getValidAgreements, sets RequestApproved, attaches response to PolicyUpdate, sends back via sidecar.")

    Component(validate, "getValidAgreements", "generate_validation_response.go", "Shared with requestApproval flow. Loops through data providers, retrieves agreements from etcd, validates user relations and archetypes. Populates ValidDataproviders / InvalidDataproviders.")
}

ContainerQueue(rabbit, "RabbitMQ", "AMQP")
ContainerDb(etcd, "etcd", "KV Store")

Rel(rabbit, router, "Consume policyUpdate from policyEnforcer-in")
Rel(router, check, "Dispatch policyUpdate")
Rel(check, validate, "Get valid agreements")
Rel(validate, etcd, "GET /policyEnforcer/agreements/{steward}")
Rel(check, rabbit, "SendPolicyUpdate → orchestrator-in (via sidecar)")

SHOW_LEGEND()
@enduml
```

### 3c — Sidecar Components

```plantuml
@startuml C4_Level3_Sidecar
!include <C4/C4_Component>

title Sidecar — Component Diagram (Level 3) — Message Bridge

Container_Boundary(sidecar, "Sidecar") {

    Component(grpcServer, "gRPC Server", "rabbit_send.go", "Implements the RabbitMQ gRPC service. Exposes RPC methods to the main service.")

    Component(sendPU, "SendPolicyUpdate", "rabbit_send.go", "Marshals PolicyUpdate protobuf, creates AMQP Publishing with correlationId and type='policyUpdate', delegates to send().")

    Component(sendCR, "SendCompositionRequest", "rabbit_send.go", "Marshals CompositionRequest protobuf, creates AMQP Publishing, delegates to send().")

    Component(send, "send", "rabbit_send.go", "Generic AMQP publish function with exponential backoff retry. Publishes to the specified destination queue.")

    Component(consume, "Consume", "gRPC stream", "Streams incoming RabbitMQ messages to the main service as SideCarMessage via gRPC server-streaming.")
}

Container(mainService, "Main Service", "Orchestrator or Policy Enforcer")
ContainerQueue(rabbit, "RabbitMQ", "AMQP")

Rel(mainService, grpcServer, "gRPC calls")
Rel(grpcServer, sendPU, "SendPolicyUpdate")
Rel(grpcServer, sendCR, "SendCompositionRequest")
Rel(sendPU, send, "Delegates")
Rel(sendCR, send, "Delegates")
Rel(send, rabbit, "AMQP Publish")
Rel(rabbit, consume, "AMQP Consume")
Rel(consume, mainService, "gRPC stream")

SHOW_LEGEND()
@enduml
```

---

## Level 4 — Code

Shows the key data structures (protobuf messages) that flow during the
policy update and the etcd key-space that underpins the system.

### 4a — Protobuf Message Structures

```plantuml
@startuml C4_Level4_Code
!theme cerulean

title Legacy Policy Update — Key Data Structures (Level 4)

class PolicyUpdate <<protobuf>> {
  +type : string            = "policyUpdate"
  +user : User
  +data_providers : string[]
  +request_metadata : RequestMetadata
  +validation_response : ValidationResponse
}

class ValidationResponse <<protobuf>> {
  +type : string                    = "policyUpdate"
  +request_type : string
  +valid_dataproviders : map<string, DataProvider>
  +invalid_dataproviders : string[]
  +auth : Auth
  +user : User
  +request_approved : bool
  +valid_archetypes : UserArchetypes
  +options : map<string, bool>
}

class CompositionRequest <<protobuf>> {
  +archetype_id : string
  +request_type : string
  +role : string
  +user : User
  +data_providers : string[]
  +destination_queue : string
  +job_name : string
  +local_job_name : string
}

class User <<protobuf>> {
  +id : string
  +user_name : string
}

class RequestMetadata <<protobuf>> {
  +correlation_id : string
  +destination_queue : string
  +job_name : string
  +return_address : string
  +job_id : string
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

class Auth <<protobuf>> {
  +access_token : string
  +refresh_token : string
}

PolicyUpdate --> User
PolicyUpdate --> RequestMetadata
PolicyUpdate --> ValidationResponse
PolicyUpdate ..> "policyEnforcer-in" : sent to (phase 2)
PolicyUpdate ..> "orchestrator-in" : sent to (phase 4)

ValidationResponse --> User
ValidationResponse --> Auth
ValidationResponse --> DataProvider : valid_dataproviders
ValidationResponse --> UserArchetypes

UserArchetypes --> "0..*" UserAllowedArchetypes : archetypes\n(map key = provider name)

CompositionRequest --> User
CompositionRequest ..> "{agent}-in" : sent to (phase 5,\nif archetype changes)

@enduml
```

### 4b — etcd Key Space

```plantuml
@startuml C4_Level4_etcd
!theme cerulean

title Legacy Policy Update — etcd Key Space (Level 4)

package "etcd Key-Value Store" {

    object "/policyEnforcer/agreements/{steward}" as agreements {
        name : string
        relations : map<userName, Relation>
        computeProviders : string[]
        archetypes : string[]
    }

    object "Relation (nested)" as relation {
        ID : string
        requestTypes : string[]
        dataSets : string[]
        allowedArchetypes : string[]
        allowedComputeProviders : string[]
    }

    object "/agents/online/{name}" as agentOnline {
        name : string
        dns : string
        routingKey : string
    }

    object "/archetypes/{name}" as archetypes {
        name : string
        computeProvider : string
        resultRecipient : string
        weight : int
    }

    object "/agents/jobs/{agent}/{user}/{job}" as jobEntry {
        archetype_id : string
        request_type : string
        role : string
        user : User
        data_providers : string[]
        destination_queue : string
        job_name : string
        local_job_name : string
    }
}

agreements --> relation : relations[userName]

note right of agreements
  **Written** by Orchestrator (HTTP PUT trigger)
  **Read** by Policy Enforcer (getValidAgreements)
end note

note right of agentOnline
  **Read** by Orchestrator
  in getAuthorizedProviders()
  and getJobAcrossAgents()
end note

note right of archetypes
  **Read** by Orchestrator
  in chooseArchetype()
  and processPolicyUpdate()
end note

note right of jobEntry
  **Read** by Orchestrator in checkJobs()
  **Written** by processPolicyUpdate()
  **Deleted** by processPolicyUpdate()
  or deleteJobInfo()
end note

@enduml
```

### 4c — Go Model Structures

```plantuml
@startuml C4_Level4_GoModels
!theme cerulean

title Legacy Policy Update — Go Models (Level 4)

class Agreement <<go/pkg/api>> {
  +Name : string
  +Relations : map[string]Relation
  +ComputeProviders : []string
  +Archetypes : []string
}

class Relation <<go/pkg/api>> {
  +ID : string
  +RequestTypes : []string
  +DataSets : []string
  +AllowedArchetypes : []string
  +AllowedComputeProviders : []string
}

class Archetype <<go/pkg/api>> {
  +Name : string
  +ComputeProvider : string
  +ResultRecipient : string
  +Weight : int
}

class AgentDetails <<go/pkg/lib>> {
  +Name : string
  +ActiveSince : *time.Time
  +ConfigUpdated : *time.Time
  +RoutingKey : string
  +Dns : string
}

Agreement --> "0..*" Relation : Relations[userName]
Agreement ..> Archetype : references via\narchetypes[] names

note bottom of Archetype
  ComputeProvider field determines
  archetype type:
  - != "other" → computeToData
  - == "other" → dataThroughTtp
end note

@enduml
```

---

## How to Render

These diagrams use the [C4-PlantUML](https://github.com/plantuml-stdlib/C4-PlantUML) standard library
which is bundled with PlantUML since version 1.2021.1. You can render them with:

- **VS Code** — install the *PlantUML* extension (`jebbs.plantuml`) and preview the fenced blocks.
- **CLI** — `java -jar plantuml.jar old_policy_update_c4.md` (PlantUML renders `plantuml` fenced blocks inside Markdown).
- **Online** — paste each block into [plantuml.com](https://www.plantuml.com/plantuml/uml).
