# Old/Legacy Policy Update Diagrams (PlantUML)

This page embeds the Policy Update diagrams as PlantUML blocks.

Each activity and sequence section starts with a **high-level overview** that
shows only the inter-service flow, followed by **per-service detail diagrams**
that zoom into the internal logic of each microservice.

> **Branch:** `legacy-policy-enforcer`
>
> These diagrams describe the **old/legacy** policy update flow. See also: [Legacy Policy Update Flow](../development_guide/legacy_policy_update_flow.md)

---

## 1. Activity Diagrams

### 1.1 High-Level Overview

```plantuml
@startuml Activity - High Level Overview
!theme cerulean

title Policy Update — High-Level Activity

start

:External Client / Admin sends\nHTTP PUT /api/v1/policyEnforcer/agreements;

partition "Orchestrator  (trigger phase)" #LightYellow {
    :Save updated agreement to etcd;
    :Evaluate active jobs affected\nby the agreement change;
    :Build & publish policyUpdate\nto RabbitMQ;
}

partition "Policy Enforcer" #LightGreen {
    :Re-validate agreements for\neach data provider in the update;
    :Attach ValidationResponse\nand publish policyUpdate back;
}

partition "Orchestrator  (processing phase)" #LightYellow {
    :Process policy update response;
    :Choose new archetype based on\nupdated validation;
    :Update / delete job entries in etcd;
    if (Archetype change requires\nnew compute provider?) then (yes)
        :Send CompositionRequest to TTP;
    else (no)
    endif
}

:Jobs updated to reflect\nnew agreement state;

stop

@enduml
```

### 1.2 Orchestrator — Trigger Phase (checkJobs)

```plantuml
@startuml Activity - Orchestrator Trigger
!theme cerulean

title Orchestrator — Trigger Phase Activity (checkJobs)

start

:Receive HTTP PUT\n/api/v1/policyEnforcer/agreements\n{Agreement JSON};

:Save agreement to etcd\n/policyEnforcer/agreements/{name}\n(GenericPutToEtcd);

:Spawn goroutine:\ngo checkJobs(agreement);

while (More relations (users)\nin agreement?) is (yes)
    :Get next relation (userName, relationDetails);
    :Query etcd for active job names\n/agents/jobs/{agreementName}/{userName};

    if (Active jobs found?) then (yes)
        if (User has allowed archetypes\nin new agreement?) then (yes)
            :Call evaluateArchetypeInActiveJobs;
        else (no)
            :Call deleteJobInfo\n(clean up all job entries);
        endif
    else (no)
        :Skip — nothing to update;
    endif
endwhile (no)

stop

@enduml
```

### 1.3 Orchestrator — evaluateArchetypeInActiveJobs

```plantuml
@startuml Activity - evaluateArchetypeInActiveJobs
!theme cerulean

title Orchestrator — evaluateArchetypeInActiveJobs Activity

start

while (More active jobs?) is (yes)
    :Get current job info from etcd\n/agents/jobs/{agent}/{user}/{job};

    :Create PolicyUpdate protobuf\n(type="policyUpdate",\nuser={id, userName},\ndestination="policyEnforcer-in");

    :Generate correlation ID (UUID);

    :Call getJobAcrossAgents\n(scan /agents/online/ and\nlookup each agent's job entry);

    while (More agents with this job?) is (yes)
        if (Agent role == "all"\nor "dataProvider"?) then (yes)
            :Add agent to\npolicyUpdate.DataProviders;
        endif
    endwhile (no)

    :Store agentsWithThisJob in\npolicyUpdateMap[correlationId];

    :Publish PolicyUpdate via\nc.SendPolicyUpdate (gRPC → sidecar);
endwhile (no)

stop

@enduml
```

### 1.4 Policy Enforcer — checkPolicyUpdate

```plantuml
@startuml Activity - Policy Enforcer
!theme cerulean

title Policy Enforcer — checkPolicyUpdate Activity

start

:Consume policyUpdate\nfrom policyEnforcer-in queue;

:Initialize ValidationResponse\n(type="policyUpdate");

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

:Set destination = "orchestrator-in";
:Set RequestApproved\n= (ValidDataproviders > 0);
:Attach ValidationResponse\nto PolicyUpdate;

:Publish PolicyUpdate via\nc.SendPolicyUpdate (gRPC → sidecar);

stop

note right
  **Potential bug:** If no agreements
  exist, the function sends the update
  before setting RequestApproved,
  resulting in a duplicate message.
end note

@enduml
```

### 1.5 Orchestrator — processPolicyUpdate

```plantuml
@startuml Activity - processPolicyUpdate
!theme cerulean

title Orchestrator — processPolicyUpdate Activity

start

:Look up agentsWithThisJob\nfrom policyUpdateMap[correlationId];
:Delete entry from policyUpdateMap;

:getAuthorizedProviders()\n(check which valid providers\nare online via /agents/online/);

:chooseArchetype()\n(select best archetype based on\noptions, weights, provider constraints);

:Get archetype config from etcd\n/archetypes/{archetype};

if (Same archetype as current?) then (yes)
    :Do nothing — return;
    stop
endif

if (New archetype == computeToData?\n(computeProvider != "other")) then (yes)
    while (More agents in job?) is (yes)
        if (Agent role == computeProvider?) then (yes)
            :Delete job entry from etcd;
        else (dataProvider / all)
            :Update role to "all";
            :Save new archetype to etcd;
            :Set computeProviderAlready = true;
        endif
    endwhile (no)
else (dataThroughTtp)
    :chooseThirdParty()\n(find common compute provider\nacross all valid data providers);

    while (More agents in job?) is (yes)
        if (Agent role == computeProvider\n&& agent == chosen TTP?) then (yes)
            :Keep — set computeProviderAlready = true;
        elseif (Agent role == computeProvider\n&& agent != chosen TTP?) then (yes)
            :Delete job entry from etcd;
        elseif (Agent role == "all"?) then (yes)
            if (Agent still a valid\ndata provider?) then (yes)
                :Update role to "dataProvider";
                :Save new archetype to etcd;
            else (no)
                :Delete job entry from etcd;
            endif
        endif
    endwhile (no)
endif

if (computeProviderAlready == false?) then (yes)
    :Create CompositionRequest\nfor TTP (role="computeProvider");
    :Send CompositionRequest via\nc.SendCompositionRequest\n(gRPC → sidecar → TTP routing key);
endif

stop

@enduml
```

---

## 2. Sequence Diagrams

### 2.1 High-Level Overview

```plantuml
@startuml Sequence - High Level Overview
!theme cerulean

title Policy Update — High-Level Sequence

actor "External Client" as Client
box "DYNAMOS Platform" #LightBlue
    participant "Orchestrator"    as Orch
    participant "Policy Enforcer" as PE
end box
database "etcd" as etcd
queue "RabbitMQ" as MQ

Client -> Orch : HTTP PUT /api/v1/policyEnforcer/agreements
Orch -> etcd : Save agreement
activate Orch
Orch -> Orch : checkJobs → evaluateArchetypeInActiveJobs
Orch ->> MQ : PolicyUpdate → "policyEnforcer-in"
deactivate Orch

MQ ->> PE : Deliver PolicyUpdate
activate PE
PE -> etcd : Look up agreements for each data provider
PE -> PE : Validate & build ValidationResponse
PE ->> MQ : PolicyUpdate (with ValidationResponse)\n→ "orchestrator-in"
deactivate PE

MQ ->> Orch : Deliver PolicyUpdate response
activate Orch
Orch -> Orch : processPolicyUpdate\n(choose archetype, update roles)
Orch -> etcd : Update / delete job entries

opt Archetype change requires new compute provider
    Orch ->> MQ : CompositionRequest → TTP agent
end

deactivate Orch

@enduml
```

### 2.2 Full Detailed Sequence

```plantuml
@startuml Sequence - Full Detail
!theme cerulean

title Policy Update — Full Detailed Sequence

actor "External Client" as Client
participant "Orchestrator\n(HTTP API)" as OrcAPI
participant "Orchestrator\n(Job Mgmt)" as OrcJobs
database "etcd" as etcd
participant "Sidecar\n(Orchestrator)" as OrcSidecar
queue "RabbitMQ" as MQ
participant "Sidecar\n(Policy Enforcer)" as PESidecar
participant "Policy Enforcer" as PE

== Phase 1: Trigger — Agreement Update ==

Client -> OrcAPI : HTTP PUT /api/v1/policyEnforcer/agreements\n{Agreement JSON}
OrcAPI -> etcd : PUT /policyEnforcer/agreements/{name}
OrcAPI --> Client : HTTP 200 OK
OrcAPI ->> OrcJobs : go checkJobs(agreement)

== Phase 2: Evaluate Active Jobs ==

loop For each relation (user) in agreement
    OrcJobs -> etcd : GET /agents/jobs/{agreementName}/{userName}\n(active job names)

    alt No active jobs
        note over OrcJobs : Skip — nothing to update
    else No allowed archetypes
        OrcJobs -> etcd : DELETE job entries (deleteJobInfo)
    else Active jobs with allowed archetypes

        loop For each active job
            OrcJobs -> etcd : GET /agents/jobs/{agent}/{user}/{job}\n(current job info)

            OrcJobs -> etcd : GET /agents/online/\n(all online agents)

            loop For each online agent
                OrcJobs -> etcd : GET /agents/jobs/{agent}/{user}/{job}\n(getJobAcrossAgents)
            end

            note over OrcJobs
                Build PolicyUpdate message
                Store agentsWithThisJob
                in policyUpdateMap[correlationId]
            end note

            OrcJobs -> OrcSidecar : gRPC: SendPolicyUpdate(policyUpdate)

            == Phase 3: Publish to RabbitMQ ==

            OrcSidecar ->> MQ : Publish to "policyEnforcer-in"\ntype: "policyUpdate"
        end
    end
end

== Phase 4: Policy Enforcer Validates ==

MQ ->> PESidecar : Deliver message
PESidecar -> PE : gRPC stream: SideCarMessage\ntype: "policyUpdate"
PE -> PE : Unmarshal PolicyUpdate

note over PE : checkPolicyUpdate

PE -> PE : Create ValidationResponse\n(type: "policyUpdate")

loop For each data provider in PolicyUpdate
    PE -> etcd : GET /policyEnforcer/agreements/{steward}

    alt Agreement not found
        note over PE : Add to invalidDataproviders
    else User not in relations
        note over PE : Add to invalidDataproviders
    else User found
        PE -> PE : Match user archetypes ∩ agreement archetypes
        alt No matching archetypes
            note over PE : Add to invalidDataproviders
        else Matching archetypes found
            note over PE : Add to validDataproviders\nwith matched archetypes\n& compute providers
        end
    end
end

PE -> PE : Set destination = "orchestrator-in"\nSet RequestApproved = (validProviders > 0)\nAttach ValidationResponse to PolicyUpdate

PE -> PESidecar : gRPC: SendPolicyUpdate(policyUpdate)
PESidecar ->> MQ : Publish to "orchestrator-in"\ntype: "policyUpdate"

== Phase 5: Orchestrator Processes Response ==

MQ ->> OrcSidecar : Deliver message
OrcSidecar -> OrcJobs : gRPC stream: SideCarMessage\ntype: "policyUpdate"

OrcJobs -> OrcJobs : Look up agentsWithThisJob\nfrom policyUpdateMap[correlationId]
OrcJobs -> OrcJobs : Delete entry from policyUpdateMap

note over OrcJobs : processPolicyUpdate

OrcJobs -> etcd : GET /agents/online/{provider}\n(getAuthorizedProviders)
OrcJobs -> OrcJobs : chooseArchetype(validationResponse)
OrcJobs -> etcd : GET /archetypes/{archetype}

alt Same archetype as before
    note over OrcJobs : No changes needed
else New archetype: computeToData
    loop For each agent in job
        alt Agent role = computeProvider
            OrcJobs -> etcd : DELETE job entry
        else Agent role = dataProvider/all
            OrcJobs -> etcd : UPDATE role to "all",\nset new archetype
        end
    end
else New archetype: dataThroughTtp
    OrcJobs -> OrcJobs : chooseThirdParty(validationResponse)
    loop For each agent in job
        alt Agent is correct TTP
            note over OrcJobs : Keep as computeProvider
        else Agent is wrong TTP
            OrcJobs -> etcd : DELETE job entry
        else Agent role = all
            OrcJobs -> etcd : UPDATE role to "dataProvider",\nset new archetype
        end
    end

    opt No compute provider assigned yet
        OrcJobs -> OrcSidecar : gRPC: SendCompositionRequest\n(to new TTP)
        OrcSidecar ->> MQ : Publish to TTP routing key
    end
end

@enduml
```

### 2.3 Orchestrator — Trigger Phase

```plantuml
@startuml Sequence - Orchestrator Trigger
!theme cerulean

title Orchestrator — Trigger Phase Sequence

actor "External Client" as Client
participant "agreementsHandler()" as Handler
participant "checkJobs()" as CheckJobs
participant "evaluateArchetype\nInActiveJobs()" as Eval
participant "getJobAcrossAgents()" as GetJobs
participant "policyUpdateMap" as Map
database "etcd" as etcd
queue "RabbitMQ\npolicyEnforcer-in" as MQ_PE

Client -> Handler : HTTP PUT /api/v1/policyEnforcer/agreements\n{Agreement JSON}
activate Handler

Handler -> etcd : PUT /policyEnforcer/agreements/{name}\n(GenericPutToEtcd)
Handler --> Client : HTTP 200 OK

Handler ->> CheckJobs : go checkJobs(agreement)
deactivate Handler
activate CheckJobs

loop For each relation in agreement
    CheckJobs -> etcd : GET keys /agents/jobs/{agreement}/{user}\n(GetKeysFromPrefix)
    etcd --> CheckJobs : jobNames[]

    alt No jobs or no allowed archetypes
        CheckJobs -> etcd : DELETE job entries
    else Jobs with archetypes exist
        CheckJobs -> Eval : evaluateArchetypeInActiveJobs()
        activate Eval

        loop For each job
            Eval -> etcd : GET /agents/jobs/{agent}/{user}/{job}
            etcd --> Eval : CompositionRequest JSON

            Eval -> GetJobs : getJobAcrossAgents()
            activate GetJobs
            GetJobs -> etcd : GET /agents/online/
            loop For each online agent
                GetJobs -> etcd : GET /agents/jobs/{agent}/{user}/{job}
            end
            GetJobs --> Eval : agentsWithThisJob map
            deactivate GetJobs

            Eval -> Map : Store agentsWithThisJob\n[correlationId]

            Eval ->> MQ_PE : SendPolicyUpdate\n(via sidecar)
        end
        deactivate Eval
    end
end

deactivate CheckJobs

@enduml
```

### 2.4 Policy Enforcer — checkPolicyUpdate

```plantuml
@startuml Sequence - Policy Enforcer
!theme cerulean

title Policy Enforcer — checkPolicyUpdate Sequence

queue "RabbitMQ\npolicyEnforcer-in" as MQ_PE
participant "handleIncomingMessages()" as Router
participant "checkPolicyUpdate()" as Check
participant "getValidAgreements()" as Validate
database "etcd" as etcd
queue "RabbitMQ\norchestrator-in" as MQ_Orch

MQ_PE -> Router : Consume policyUpdate
activate Router

Router -> Check : Dispatch (type = "policyUpdate")
activate Check

Check -> Validate : getValidAgreements(dataProviders, user)
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

Check -> Check : Set destination = "orchestrator-in"\nAttach ValidationResponse to PolicyUpdate

Check ->> MQ_Orch : SendPolicyUpdate\n(via sidecar → orchestrator-in)
deactivate Check
deactivate Router

@enduml
```

### 2.5 Orchestrator — processPolicyUpdate

```plantuml
@startuml Sequence - Orchestrator Process
!theme cerulean

title Orchestrator — processPolicyUpdate Sequence

queue "RabbitMQ\norchestrator-in" as MQ_Orch
participant "handleIncomingMessages()" as Router
participant "policyUpdateMap" as Map
participant "processPolicyUpdate()" as Process
participant "getAuthorizedProviders()" as AuthProv
participant "chooseArchetype()" as ChooseArch
participant "chooseThirdParty()" as ChooseTTP
database "etcd" as etcd
queue "RabbitMQ\n{agent}-in" as MQ_Agent

MQ_Orch -> Router : Consume policyUpdate
activate Router

Router -> Map : Lookup agentsWithThisJob\n[correlationId]
Map --> Router : agentsWithThisJob
Router -> Map : Delete entry

Router -> Process : processPolicyUpdate(\nagentsWithThisJob, policyUpdate)
activate Process

Process -> AuthProv : getAuthorizedProviders()
activate AuthProv
loop For each ValidDataprovider
    AuthProv -> etcd : GET /agents/online/{provider}
    etcd --> AuthProv : AgentDetails (if online)
end
AuthProv --> Process : authorizedProviders[]
deactivate AuthProv

Process -> ChooseArch : chooseArchetype()
activate ChooseArch
ChooseArch -> etcd : GET /archetypes/ (weight-based)
ChooseArch --> Process : selected archetype
deactivate ChooseArch

Process -> etcd : GET /archetypes/{archetype}\n(config)

alt Same archetype as current
    note over Process : No changes needed — return
else computeToData (computeProvider != "other")
    loop For each agent in job
        alt role == computeProvider
            Process -> etcd : DELETE /agents/jobs/{agent}/{user}/{job}
        else role == dataProvider or all
            Process -> etcd : PUT /agents/jobs/{agent}/{user}/{job}\n(role="all", new archetype)
        end
    end
else dataThroughTtp (computeProvider == "other")
    Process -> ChooseTTP : chooseThirdParty()
    activate ChooseTTP
    ChooseTTP -> etcd : GET /agents/online/{ttp}
    ChooseTTP --> Process : TTP AgentDetails
    deactivate ChooseTTP

    loop For each agent in job
        alt computeProvider == chosen TTP
            note over Process : Keep — no change
        else computeProvider != chosen TTP
            Process -> etcd : DELETE job entry
        else role == all
            Process -> etcd : PUT job entry\n(role="dataProvider", new archetype)
        end
    end

    opt No compute provider assigned yet
        Process ->> MQ_Agent : SendCompositionRequest\n(role="computeProvider",\ndest=TTP routing key)
    end
end

deactivate Process
deactivate Router

@enduml
```

---

## 3. Architecture Diagram

A static view of the DYNAMOS components involved in the policy update flow.

```plantuml
@startuml Architecture Diagram
!theme cerulean

title DYNAMOS — Policy Update Architecture Overview

skinparam component {
    BackgroundColor<<steward>> #D5E8D4
    BorderColor<<steward>> #82B366
}

actor "External Client / Admin" as admin

rectangle "DYNAMOS Platform" {

    component "Orchestrator" as orch

    component "Policy Enforcer" as pe

    component "Data Steward" as ds3 <<steward>> #white/Green
    component "Data Steward" as ds2 <<steward>> #white/green
    component "Data Steward" as ds1 <<steward>> #white/Green

    database "etcd\n(Key-Value Store)" as etcd

    queue "RabbitMQ\n(Message Broker)" as rabbit
}

' External actor
admin --> orch : HTTP PUT\n/agreements

' Inter-service communication via RabbitMQ
pe -- rabbit
orch -- rabbit
ds1 -- rabbit
ds2 -- rabbit
ds3 -- rabbit

' etcd access
pe -- etcd
orch -- etcd

@enduml
```

---

## 4. Component Diagram

```plantuml
@startuml Policy Update Component Diagram
!theme cerulean

title DYNAMOS - Policy Update Components

actor "External Client" as client

package "HTTP API Layer" {
    component "Orchestrator\n(HTTP Server)" as orchHttp
}

package "Core Services" {
    component "Orchestrator\n(Job Management)" as orchJobs
    component "Policy Enforcer" as pe
}

package "Message Broker" {
    queue "policyEnforcer-in" as q_pe
    queue "orchestrator-in" as q_orch
    queue "{agent}-in" as q_agent
}

package "Data Stores" {
    database "etcd" as etcd
}

package "Data Providers" {
    component "Agent (TTP)" as agent_ttp
}

' Client connections
client --> orchHttp : HTTP PUT\n/api/v1/policyEnforcer/agreements

' Orchestrator trigger flows
orchHttp --> etcd : Save agreement
orchHttp --> orchJobs : go checkJobs()
orchJobs --> etcd : Read jobs / agents
orchJobs --> q_pe : SendPolicyUpdate

' Policy Enforcer flows
q_pe --> pe : Consume\npolicyUpdate
pe --> etcd : GET /policyEnforcer/agreements
pe --> q_orch : SendPolicyUpdate\n(with ValidationResponse)

' Orchestrator processing flows
q_orch --> orchJobs : Consume\npolicyUpdate (response)
orchJobs --> etcd : Update/delete\njob entries
orchJobs --> q_agent : SendCompositionRequest\n(if TTP needed)

' Agent queues
q_agent --> agent_ttp

@enduml
```

---

## 5. Message Content Diagram — PolicyUpdate Lifecycle

Shows how the `PolicyUpdate` message content evolves as it passes through the system.

```plantuml
@startuml PolicyUpdate Lifecycle
!theme cerulean

title PolicyUpdate Message — Lifecycle

rectangle "Orchestrator Creates" as OrcCreates {
    card "**PolicyUpdate**\n----\ntype: \"policyUpdate\"\nuser: {id, userName}\ndata_providers: [agent1, agent2]\nrequest_metadata:\n  correlationId: UUID\n  destinationQueue: \"policyEnforcer-in\"\nvalidation_response: //(empty)//" as MsgA
}

rectangle "Policy Enforcer Enriches" as PEEnriches {
    card "**PolicyUpdate**\n----\ntype: \"policyUpdate\"\nuser: {id, userName}\ndata_providers: [agent1, agent2]\nrequest_metadata:\n  correlationId: UUID\n  destinationQueue: \"orchestrator-in\"\nvalidation_response:\n  type: \"policyUpdate\"\n  valid_dataproviders: {agent1: {...}}\n  invalid_dataproviders: [agent2]\n  request_approved: true/false\n  valid_archetypes: {agent1: [...]}" as MsgB
}

rectangle "Orchestrator Processes" as OrcProcesses {
    card "Orchestrator uses the enriched\nPolicyUpdate to choose a new\narchetype and update job\nconfigurations in etcd." as MsgC
}

MsgA -right-> MsgB : via RabbitMQ\n(policyEnforcer-in)
MsgB -right-> MsgC : via RabbitMQ\n(orchestrator-in)

@enduml
```

---

## 6. etcd Data Flow Diagram

Shows which etcd paths are read/written at each stage of the policy update.

```plantuml
@startuml etcd Data Flow
!theme cerulean

title Policy Update — etcd Data Flow

rectangle "Phase 1: HTTP Trigger" #LightYellow {
    storage "WRITE\n/policyEnforcer/agreements/{name}" as W1
}

rectangle "Phase 2: Orchestrator evaluates" #LightYellow {
    storage "READ\n/agents/jobs/{agreement}/{user}" as R1
    storage "READ\n/agents/jobs/{agent}/{user}/{job}" as R2
    storage "READ\n/agents/online/" as R3
    storage "DELETE\njob entries (if no archetypes)" as D1
}

rectangle "Phase 4: Policy Enforcer validates" #LightGreen {
    storage "READ\n/policyEnforcer/agreements/{steward}" as R4
}

rectangle "Phase 5: Orchestrator processes" #LightYellow {
    storage "READ\n/agents/online/{provider}" as R5
    storage "READ\n/archetypes/{archetype}" as R6
    storage "WRITE\n/agents/jobs/{agent}/{user}/{job}\n(update role & archetype)" as W2
    storage "DELETE\n/agents/jobs/{agent}/{user}/{job}\n(obsolete entries)" as D2
}

W1 --> R1
R1 --> R2
R2 --> R3
R1 ..> D1 : if no\narchetypes

R4 ..> R5
R5 --> R6
R6 --> W2
R6 --> D2

@enduml
```
