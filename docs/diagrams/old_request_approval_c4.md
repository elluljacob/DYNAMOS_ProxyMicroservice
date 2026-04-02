# C4 Diagrams — Legacy Request Approval Flow

This document describes the legacy DYNAMOS Request Approval flow using the
[C4 model](https://c4model.com/) (System Context, Container, Component, Code).
Each level is embedded as a PlantUML block that uses the
[C4-PlantUML](https://github.com/plantuml-stdlib/C4-PlantUML) standard library.

---

## Level 1 — System Context

Shows DYNAMOS as a single system box in the context of its users and any
external systems it depends on.

```plantuml
@startuml C4_Level1_SystemContext
!include <C4/C4_Context>

title DYNAMOS — System Context Diagram (Level 1)

Person(analyst, "Data Analyst", "Submits data requests and receives aggregated results.")

System(dynamos, "DYNAMOS Platform", "Validates data-access requests against organisational agreements, orchestrates approved jobs, and dispatches work to data-provider agents.")

System_Ext(agents, "Data-Provider Agents", "Autonomous agents (VU, UVA, SURF, …) that expose data sets and compute capabilities on behalf of their organisations.")

Rel(analyst, dynamos, "Sends requestApproval & receives results", "HTTP / JSON")
Rel(dynamos, agents, "Dispatches data requests to authorised agents", "HTTP POST")

SHOW_LEGEND()
@enduml
```

---

## Level 2 — Container

Zooms into the DYNAMOS system to reveal its main runtime containers and the
technologies that connect them.

```plantuml
@startuml C4_Level2_Container
!include <C4/C4_Container>

title DYNAMOS — Container Diagram (Level 2)

Person(analyst, "Data Analyst", "Submits data requests.")

System_Boundary(dynamos, "DYNAMOS Platform") {

    Container(api, "API Gateway", "Go", "Accepts HTTP requests, publishes to RabbitMQ, waits for approval, then forwards data requests to agents.")

    Container(pe, "Policy Enforcer", "Go", "Validates requests against organisational agreements stored in etcd. Produces a ValidationResponse.")

    Container(orch, "Orchestrator", "Go", "Checks agent availability, selects an archetype, starts composition requests, and builds the final approval response.")

    ContainerQueue(rabbit, "RabbitMQ", "AMQP", "Message broker with per-service queues: policyEnforcer-in, orchestrator-in, api-gateway-in, {agent}-in.")

    ContainerDb(etcd, "etcd", "Key-Value Store", "Stores agreements, agent-online status, and archetype configurations.")
}

System_Ext(agents, "Data-Provider Agents", "VU, UVA, SURF — expose data/compute endpoints.")

Rel(analyst, api, "HTTP POST /api/v1/requestApproval", "JSON")

Rel(api, rabbit, "Publish requestApproval", "policyEnforcer-in")
Rel(rabbit, pe, "Consume requestApproval", "AMQP")

Rel(pe, etcd, "GET /policyEnforcer/agreements/{steward}", "HTTP / gRPC")
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

    Component(router, "handleIncomingMessages", "Go func", "RabbitMQ consumer that routes messages by type. Dispatches 'requestApproval' to checkRequestApproval.")

    Component(check, "checkRequestApproval", "Go func", "Entry point: calls getValidAgreements, sets RequestApproved flag, generates auth token, sends ValidationResponse.")

    Component(validate, "getValidAgreements", "Go func", "Loops through requested data providers, retrieves agreements from etcd, validates user relations and archetypes. Populates ValidDataproviders / InvalidDataproviders.")

    Component(auth, "Auth Token Generator", "Go func", "Creates a signed authentication token included in the ValidationResponse.")
}

ContainerQueue(rabbit, "RabbitMQ", "AMQP")
ContainerDb(etcd, "etcd", "KV Store")

Rel(rabbit, router, "Consume requestApproval from policyEnforcer-in")
Rel(router, check, "Dispatch requestApproval")
Rel(check, validate, "Get valid agreements")
Rel(validate, etcd, "GET /policyEnforcer/agreements/{steward}")
Rel(check, auth, "Generate auth token")
Rel(check, rabbit, "Publish validationResponse → orchestrator-in (hardcoded)")

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
containers and the etcd key-space that underpins the system. This level is
typically generated from code; here we present it as a class diagram derived
from the legacy protobuf definitions.

### 4a — Protobuf Message Structures

```plantuml
@startuml C4_Level4_Code
!theme cerulean

title Legacy Request Approval — Key Data Structures (Level 4)

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
  +role : string
  +archetype : string
  +job_name : string
  +provider : string
}

class User <<protobuf>> {
  +id : string
  +user_name : string
}

class Auth <<protobuf>> {
  +token : string
}

class DataProvider <<protobuf>> {
  +name : string
  +archetypes : string[]
  +compute_providers : string[]
}

class UserArchetypes <<protobuf>> {
  +archetypes : map<string, ArchetypeDetail>
}

class RequestMetadata <<protobuf>> {
  +destination_queue : string = "api-gateway-in"
  +job_id : string
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

CompositionRequest ..> "{agent}-in" : published to

@enduml
```

### 4a-i — ValidationResponse Full Structure

A standalone diagram showing the complete `ValidationResponse` protobuf message
and all of its nested types in a single view.

```plantuml
@startuml C4_Level4_ValidationResponse_FullStructure
!theme cerulean

title ValidationResponse — Full Protobuf Structure

class ValidationResponse <<protobuf>> {
  +type : string                          = "validationResponse"
  +request_type : string
  +valid_dataproviders : map<string, DataProvider>
  +invalid_dataproviders : string[]
  +auth : Auth
  +user : User
  +request_approved : bool
  +valid_archetypes : UserArchetypes
  +options : map<string, bool>
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

ValidationResponse --> User : user
ValidationResponse --> Auth : auth
ValidationResponse --> "0..*" DataProvider : valid_dataproviders\n(map key = provider name)
ValidationResponse --> UserArchetypes : valid_archetypes

UserArchetypes --> "0..*" UserAllowedArchetypes : archetypes\n(map key = provider name)

@enduml
```

### 4b — etcd Key Space

```plantuml
@startuml C4_Level4_etcd
!theme cerulean

title Legacy Request Approval — etcd Key Space (Level 4)

package "etcd Key-Value Store" {

    object "/policyEnforcer/agreements/{steward}" as agreements {
        name : string
        relations : map<userId, Relation>
        archetypes : string[]
        computeProviders : string[]
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
  Read by **Policy Enforcer**
  in getValidAgreements()
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
