# Request Approval Diagrams (PlantUML)

This page embeds the Request Approval diagrams as PlantUML blocks.

Each activity and sequence section starts with a **high-level overview** that
shows only the inter-service flow, followed by **per-service detail diagrams**
that zoom into the internal logic of each microservice.

---

## 1. Activity Diagrams

### 1.1 High-Level Overview

```plantuml
@startuml Activity - High Level Overview
!theme cerulean

title Request Approval — High-Level Activity

start

:Data Analyst sends HTTP POST\n/api/v1/requestApproval;

partition "API Gateway  (request phase)" #LightBlue {
    :Parse request & publish\nrequestApproval to RabbitMQ;
}

partition "Policy Enforcer" #LightGreen {
    :Validate agreements for\neach requested data provider;
    :Generate auth token &\npublish validationResponse;
}

partition "Orchestrator" #LightYellow {
    :Check provider availability,\nchoose archetype & send\ncompositionRequests;
    :Publish requestApprovalResponse;
}

partition "API Gateway  (response phase)" #LightBlue {
    :Forward data requests to\nauthorised agents;
}

:Data Analyst receives\naggregated response;

stop

@enduml
```

### 1.2 API Gateway — Request Phase

```plantuml
@startuml Activity - API Gateway Request
!theme cerulean

title API Gateway — Request Phase Activity

start

:Receive HTTP POST\n/api/v1/requestApproval\n{type, user, dataProviders, dataRequest};

:Parse HTTP request body;

:Create RequestApproval protobuf\n(set DestinationQueue = "policyEnforcer-in");

:Store response channel\nin requestApprovalMap[user.Id];

:Publish requestApproval\nto RabbitMQ (policyEnforcer-in);

:Block — wait on response channel;

stop

note right
  The goroutine blocks here until
  handleIncomingMessages() pushes
  a requestApprovalResponse onto
  the channel (see Response Phase).
end note

@enduml
```

### 1.3 Policy Enforcer

```plantuml
@startuml Activity - Policy Enforcer
!theme cerulean

title Policy Enforcer — Activity

start

:Consume requestApproval\nfrom policyEnforcer-in queue;

:Initialize ValidationResponse;

while (More data providers?) is (yes)
    :Get next steward;
    :Query etcd\n/policyEnforcer/agreements/{steward};

    if (Agreement exists?) then (yes)
        :Get user relation from agreement;

        if (User in relations?) then (yes)
            :Match user archetypes\nwith agreement archetypes;

            if (Matching archetypes?) then (yes)
                :Add to ValidDataproviders;
                :Store matched archetypes;
                :Store compute providers;
                :Add agreement to list;
            else (no)
                :Add to InvalidDataproviders;
            endif
        else (no)
            :Add to InvalidDataproviders;
        endif
    else (no)
        :Add to InvalidDataproviders;
    endif
endwhile (no)

if (ValidDataproviders > 0?) then (yes)
    :Set RequestApproved = true;
else (no)
    :Set RequestApproved = false;
endif

:Generate Auth token;

:Publish validationResponse\nto orchestrator-in (hardcoded);

stop

@enduml
```

### 1.4 Orchestrator

```plantuml
@startuml Activity - Orchestrator
!theme cerulean

title Orchestrator — Activity

start

:Consume validationResponse\nfrom orchestrator-in queue;

:Initialize RequestApprovalResponse;

while (More valid providers?) is (yes)
    :Query etcd\n/agents/online/{provider};

    if (Agent online?) then (yes)
        :Add to authorizedProviders;
    endif
endwhile (no)

if (authorizedProviders > 0?) then (yes)
    :Choose optimal archetype\n(chooseArchetype);
    :Get archetype config from etcd\n/archetypes/{archetype};
    :Generate job name;

    if (computeToData?) then (yes)
        :Send CompositionRequest\n(role=all) to each provider;
    else (dataThroughTtp)
        :Send CompositionRequest\n(role=dataProvider) to data providers;
        :Choose TTP compute provider;
        :Send CompositionRequest\n(role=computeProvider) to TTP;
    endif

    :Build success response\nwith authorizedProviders, jobId;
else (no)
    :Build error response;
endif

:Publish requestApprovalResponse\nto api-gateway-in;

stop

@enduml
```

### 1.5 API Gateway — Response Phase

```plantuml
@startuml Activity - API Gateway Response
!theme cerulean

title API Gateway — Response Phase Activity

start

:Consume requestApprovalResponse\nfrom api-gateway-in queue;

:Lookup channel in\nrequestApprovalMap[user.Id];

:Push response onto channel\n(unblocks requestHandler);

if (Request approved?) then (yes)
    :Add requestMetadata to dataRequest;
    :Add trace context;

    while (More authorized providers?) is (yes)
        :Send HTTP POST to agent\n/agent/v1/{type}/{target};
        :Collect response;
    endwhile (no)

    :Aggregate responses;
    :Return HTTP 200\n{jobId, responses[]};
else (no)
    :Return HTTP error;
endif

stop

@enduml
```

### 1.6 Agent

```plantuml
@startuml Activity - Agent
!theme cerulean

title Agent — Activity

start

:Consume compositionRequest\nfrom {agent}-in queue;

:Deploy microservices for job\n(based on role & archetype config);

note right
  Agents also receive the direct HTTP
  data-request from the API Gateway.
end note

:Receive HTTP POST\n/agent/v1/{type}/{target}\nwith dataRequest + metadata;

:Process data request\n(execute query / computation);

:Return response to API Gateway;

stop

@enduml
```

---

## 2. Sequence Diagrams

### 2.1 High-Level Overview

```plantuml
@startuml Sequence - High Level Overview
!theme cerulean

title Request Approval — High-Level Sequence

actor "Data Analyst" as Client
box "DYNAMOS Platform" #LightBlue
    participant "API Gateway"     as API
    participant "Policy Enforcer" as PE
    participant "Orchestrator"    as Orch
end box
participant "Agent(s)" as Agents

Client -> API : HTTP POST /api/v1/requestApproval

API ->> PE   : requestApproval  (via RabbitMQ)
activate PE
PE ->> Orch  : validationResponse  (via RabbitMQ)
deactivate PE
activate Orch
Orch ->> Agents : compositionRequest  (via RabbitMQ)
Orch ->> API : requestApprovalResponse  (via RabbitMQ)
deactivate Orch

loop each authorised provider
    API -> Agents  : HTTP POST data request
    Agents --> API : response
end

API --> Client : HTTP 200 {jobId, responses[]}

@enduml
```

### 2.2 API Gateway — Request Phase

```plantuml
@startuml Sequence - API Gateway Request
!theme cerulean

title API Gateway — Request Phase Sequence

actor "Data Analyst" as Client
participant "requestHandler()" as Handler
participant "requestApprovalMap" as Map
queue "RabbitMQ\npolicyEnforcer-in" as MQ_PE

Client -> Handler : HTTP POST /api/v1/requestApproval\n{type, user, dataProviders, dataRequest}
activate Handler

Handler -> Handler : Parse request body\nCreate RequestApproval protobuf

Handler -> Map : Store response channel\nmap[user.Id] = chan

Handler ->> MQ_PE : Publish requestApproval\n(DestinationQueue = "policyEnforcer-in")

Handler -> Handler : Block on <-chan\n(waiting for approval response)

note right of Handler
  The goroutine stays blocked here
  until the Response Phase pushes
  a message onto the channel.
end note

@enduml
```

### 2.3 Policy Enforcer

```plantuml
@startuml Sequence - Policy Enforcer
!theme cerulean

title Policy Enforcer — Sequence

queue "RabbitMQ\npolicyEnforcer-in" as MQ_PE
participant "handleIncomingMessages()" as Router
participant "checkRequestApproval()" as Check
participant "getValidAgreements()" as Validate
database "etcd" as etcd
participant "Auth Generator" as Auth
queue "RabbitMQ\norchestrator-in" as MQ_Orch

MQ_PE -> Router : Consume requestApproval
activate Router

Router -> Check : Dispatch (type = "requestApproval")
activate Check

Check -> Validate : getValidAgreements(request)
activate Validate

loop For each dataProvider (steward)
    Validate -> etcd : GET /policyEnforcer/agreements/{steward}
    etcd --> Validate : Agreement JSON

    alt Agreement exists & user in relations & archetypes match
        Validate -> Validate : Add to ValidDataproviders\nStore archetypes & compute providers
    else Otherwise
        Validate -> Validate : Add to InvalidDataproviders
    end
end

Validate --> Check : ValidDataproviders,\nInvalidDataproviders
deactivate Validate

Check -> Check : Set RequestApproved\n= len(Valid) > 0

Check -> Auth : Generate auth token
Auth --> Check : Auth token

Check ->> MQ_Orch : Publish validationResponse\n(destination hardcoded: "orchestrator-in")
deactivate Check
deactivate Router

@enduml
```

### 2.4 Orchestrator

```plantuml
@startuml Sequence - Orchestrator
!theme cerulean

title Orchestrator — Sequence

queue "RabbitMQ\norchestrator-in" as MQ_Orch
participant "handleIncomingMessages()" as Router
participant "handleRequestApproval()" as Handle
participant "getAuthorizedProviders()" as AuthProv
participant "startCompositionRequest()" as Compose
database "etcd" as etcd
queue "RabbitMQ\n{agent}-in" as MQ_Agent
queue "RabbitMQ\napi-gateway-in" as MQ_API

MQ_Orch -> Router : Consume validationResponse
activate Router

Router -> Handle : Dispatch (type = "validationResponse")
activate Handle

Handle -> AuthProv : getAuthorizedProviders()
activate AuthProv

loop For each ValidDataprovider
    AuthProv -> etcd : GET /agents/online/{provider}
    etcd --> AuthProv : AgentDetails (if online)
end

AuthProv --> Handle : authorizedProviders[]
deactivate AuthProv

alt authorizedProviders > 0
    Handle -> Compose : startCompositionRequest()
    activate Compose

    Compose -> Compose : chooseArchetype()
    Compose -> etcd : GET /archetypes/{archetype}
    etcd --> Compose : Archetype config
    Compose -> Compose : Generate jobName

    alt computeToData
        loop each authorized provider
            Compose ->> MQ_Agent : compositionRequest (role = "all")
        end
    else dataThroughTtp
        loop each data provider
            Compose ->> MQ_Agent : compositionRequest (role = "dataProvider")
        end
        Compose ->> MQ_Agent : compositionRequest to TTP (role = "computeProvider")
    end

    deactivate Compose

    Handle ->> MQ_API : requestApprovalResponse\n{authorizedProviders, jobId, auth}
else no authorized providers
    Handle ->> MQ_API : requestApprovalResponse\n{error: "no providers found"}
end

deactivate Handle
deactivate Router

@enduml
```

### 2.5 API Gateway — Response Phase

```plantuml
@startuml Sequence - API Gateway Response
!theme cerulean

title API Gateway — Response Phase Sequence

queue "RabbitMQ\napi-gateway-in" as MQ_API
participant "handleIncomingMessages()" as Incoming
participant "requestApprovalMap" as Map
participant "requestHandler()\n(blocked goroutine)" as Handler
participant "sendDataToAuthProviders()" as Send
participant "Agent(s)" as Agents
actor "Data Analyst" as Client

MQ_API -> Incoming : Consume requestApprovalResponse
activate Incoming

Incoming -> Map : Lookup channel\nfor user.Id
Map --> Incoming : chan

Incoming -> Handler : Push response onto chan\n(unblocks select)
deactivate Incoming
activate Handler

Handler -> Handler : Add requestMetadata\nAdd trace context

Handler -> Send : sendDataToAuthProviders()
activate Send

loop each authorizedProvider (parallel goroutines)
    Send -> Agents : HTTP POST /agent/v1/{type}/{target}\n{dataRequest + metadata}
    Agents --> Send : Response
end

Send --> Handler : Aggregated responses
deactivate Send

Handler --> Client : HTTP 200\n{jobId, responses[]}
deactivate Handler

@enduml
```

### 2.6 Agent

```plantuml
@startuml Sequence - Agent
!theme cerulean

title Agent — Sequence

queue "RabbitMQ\n{agent}-in" as MQ_Agent
participant "Agent\nhandleIncomingMessages()" as AgentRouter
participant "Agent\nComposition Handler" as Composition
participant "Agent\nData Handler" as DataHandler
participant "Microservices\n(sidecar, algorithm, etc.)" as Services

== Composition Phase ==

MQ_Agent -> AgentRouter : Consume compositionRequest
activate AgentRouter
AgentRouter -> Composition : Dispatch compositionRequest
activate Composition
Composition -> Services : Deploy / configure microservices\nbased on role & archetype
Services --> Composition : Ready
deactivate Composition
deactivate AgentRouter

== Data Processing Phase ==

-> DataHandler : HTTP POST /agent/v1/{type}/{target}\n(from API Gateway)
activate DataHandler
DataHandler -> Services : Forward data request
Services --> DataHandler : Processed result
<-- DataHandler : HTTP response
deactivate DataHandler

@enduml
```

---

## 3. Architecture Diagram

A static view of how the DYNAMOS components relate to each other, without
considering any specific flow.

```plantuml
@startuml Architecture Diagram
!theme cerulean

title DYNAMOS — Architecture Overview

skinparam component {
    BackgroundColor<<steward>> #D5E8D4
    BorderColor<<steward>> #82B366
}

actor "Data Analyst" as analyst

rectangle "DYNAMOS Platform" {

    component "API Gateway" as api

    component "Orchestrator" as orch

    component "Policy Enforcer" as pe

    component "Data Steward" as ds3 <<steward>> #white/Green
    component "Data Steward" as ds2 <<steward>> #white/green
    component "Data Steward" as ds1 <<steward>> #white/Green

    database "etcd\n(Key-Value Store)" as etcd

    queue "RabbitMQ\n(Message Broker)" as rabbit
}

' External actor
analyst --> api : HTTP

' Inter-service communication via RabbitMQ
api -- rabbit
pe -- rabbit
orch -- rabbit
ds1 -- rabbit
ds2 -- rabbit
ds3 -- rabbit

' etcd access
pe -- etcd
orch -- etcd

' Direct HTTP from API Gateway to agents
api --> ds1 : HTTP
api --> ds2 : HTTP
api --> ds3 : HTTP

@enduml
```

---

## 4. Component Diagram

```plantuml
@startuml Request Approval Component Diagram
!theme cerulean

title DYNAMOS - Request Approval Components

actor "Data Analyst" as client

package "API Layer" {
    component "API Gateway" as api
}

package "Core Services" {
    component "Policy Enforcer" as pe
    component "Orchestrator" as orch
}

package "Message Broker" {
    queue "policyEnforcer-in" as q_pe
    queue "orchestrator-in" as q_orch  
    queue "api-gateway-in" as q_api
    queue "{agent}-in" as q_agent
}

package "Data Stores" {
    database "etcd" as etcd
}

package "Data Providers" {
    component "Agent VU" as agent_vu
    component "Agent UVA" as agent_uva
    component "Agent SURF" as agent_surf
}

' Client connections
client --> api : HTTP POST\n/api/v1/requestApproval

' API Gateway flows
api --> q_pe : SendRequestApproval
q_api --> api : Consume\nrequestApprovalResponse
api --> agent_vu : HTTP POST\n(data request)
api --> agent_uva : HTTP POST\n(data request)

' Policy Enforcer flows
q_pe --> pe : Consume\nrequestApproval
pe --> q_orch : SendValidationResponse
pe --> etcd : GET /policyEnforcer/agreements

' Orchestrator flows
q_orch --> orch : Consume\nvalidationResponse
orch --> q_api : SendRequestApprovalResponse
orch --> q_agent : SendCompositionRequest
orch --> etcd : GET /agents/online\nGET /archetypes

' Agent queues
q_agent --> agent_vu
q_agent --> agent_uva
q_agent --> agent_surf

@enduml
```
