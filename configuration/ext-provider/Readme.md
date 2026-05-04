# ext-provider Setup Guide

ext-provider is a proxy microservice that allows DYNAMOS to treat Snowflake 
as a standard policy-compliant agent. It registers with the DYNAMOS orchestrator 
via etcd and forwards SQL and Python (Snowpark) requests to Snowflake.

## Prerequisites

- DYNAMOS fully deployed and running (see root README)
- A Snowflake account (Standard edition or higher)
- A Docker Hub account
- Go 1.21+

## Step 1: Build and push your own images

The Helm charts reference a Docker Hub account via the `dockerArtifactAccount` 
value. You must build the images yourself and push them to your own Docker Hub.

First, log in to Docker Hub:

```bash
docker login
```

Then build and push all images from the DYNAMOS root:

```bash
# Build all Go services
cd go
make all

# Build all Python services  
cd ../python
make all
```

This pushes images tagged with your branch name to your Docker Hub account.

Then update the Helm chart values to point to your account. In 
`charts/ext-provider/values.yaml`:

```yaml
dockerArtifactAccount: your-dockerhub-username
```

Replace every occurrence of the original account name (e.g. `jorrit05`) 
with your own username across all chart `values.yaml` files.

## Step 2: Configure Snowflake credentials

Create a Kubernetes secret with your Snowflake DSN:

```bash
kubectl create secret generic snowflake-credentials \
  --from-literal=dsn="USERNAME:PASSWORD@ACCOUNT/DATABASE/SCHEMA?warehouse=WAREHOUSE" \
  -n ext-provider
```

Or set it directly in the Helm values (not recommended for production):

```yaml
env:
  SNOWFLAKE_DSN: "USERNAME:PASSWORD@ACCOUNT/DATABASE/SCHEMA?warehouse=WAREHOUSE"
```

## Step 3: Set up Snowflake datasets

Upload your datasets to Snowflake and ensure the following tables exist 
in your configured database and schema:

- `PERSONEN`
- `AANSTELLINGEN`
- `USER_ACCESS` (maps user emails to institution codes)

These can be found in the `datasets` directory.


## Step 4: Deploy DYNAMOS

From inside the `configuration` directory, run:

```bash
./dynamos-configuration.sh
```

This script installs all Helm charts and pushes the required 
configuration to etcd and RabbitMQ. Once it completes, verify 
that all pods are running before submitting any requests:

```bash
kubectl get pods --all-namespaces
```

Or use k9s for a live view. Every pod should reach `Running` status 
with all containers ready (e.g. `3/3`) before the system is usable. 
Pods in `Init`, `Pending`, or `CrashLoopBackOff` indicate a 
deployment issue — see the relevant troubleshooting sections below.

---

## Monitoring with Prometheus and Grafana

DYNAMOS ships with Prometheus and Grafana for cluster resource 
monitoring. Both are deployed in the `core` namespace.

### Accessing Grafana

```bash
kubectl port-forward -n core deployment/grafana 3000:3000
```

Then open: `http://localhost:3000`

### Importing a dashboard

A pre-built Grafana dashboard used during the thesis evaluation is 
available at `configuration/grafana/cloud-resources-template.json`. 
To import it:

1. Open Grafana at `http://localhost:3000`
2. Go to **Dashboards** > **Import**
3. Click **Upload JSON file** and select `cloud-resources-template.json`
4. Click **Import**

The dashboard includes panels for CPU utilisation, memory working 
set, and node network throughput across the `ext-provider`, `vu`, 
and `surf` namespaces.

## Policy mode

ext-provider supports two policy modes controlled by the `POLICY_MODE` 
environment variable in the Helm chart:

| Value | Description |
|-------|-------------|
| `inspired` (default) | ODRL-inspired format with masking and row filters |
| `strict` | Strict ODRL column-level permissions, no masking |

See [POLICY.md](./POLICY.md) for full details on both formats.

## Example requests

Basic researcher request:
```bash
curl --location \
  'http://api-gateway.api-gateway.svc.cluster.local:80/api/v1/requestApproval' \
  --header 'Content-Type: application/json' \
  --data-raw '{
    "type": "sqlDataRequest",
    "user": {"id": "1", "userName": "jorrit.stutterheim@cloudnation.nl"},
    "dataProviders": ["EXT-PROVIDER"],
    "data_request": {
        "type": "sqlDataRequest",
        "query": "SELECT * FROM PERSONEN LIMIT 10",
        "algorithm": "aggregate",
        "options": {"graph": false, "aggregate": true},
        "requestMetadata": {}
    }
}'
```

Python request:
```bash
time curl -v --location 'http://api-gateway.api-gateway.svc.cluster.local:80/api/v1/requestApproval' \
--header 'Content-Type: application/json' \
--data-raw '{
    "type": "pythonDataRequest",
    "user": {"id": "1", "userName": "jorrit.stutterheim@cloudnation.nl"},
    "dataProviders": ["EXT-PROVIDER"],
    "data_request": {
        "type": "pythonDataRequest",
        "python_code": "def run(session):\n    df = session.table(\"PERSONEN\").join(session.table(\"AANSTELLINGEN\"), on=\"UNIEKNR\").limit(30000)\n    return df.to_pandas().to_json(orient=\"records\")",
        "algorithm": "average",
        "options": {"graph": false, "aggregate": false},
        "requestMetadata": {}
    }
}'
```

Request forcing dataThroughTtp archetype (one in readme is computeToData):
```bash
time curl -v --location 'http://api-gateway.api-gateway.svc.cluster.local:80/api/v1/requestApproval' \
--header 'Content-Type: application/json' \
--data-raw '{
    "type": "sqlDataRequest",
    "user": {"id": "1", "userName": "jorrit.stutterheim@cloudnation.nl"},
    "dataProviders": ["UVA"],
    "data_request": {
        "type": "sqlDataRequest",
        "query": "SELECT * FROM Personen p JOIN Aanstellingen s LIMIT 10000",
        "algorithm": "average",
        "options": {"graph": true, "aggregate": true},
        "requestMetadata": {}
    }
}'
```

Basic Data_Steward request:
```bash
time curl -v --location 'http://api-gateway.api-gateway.svc.cluster.local:80/api/v1/requestApproval' \
--header 'Content-Type: application/json' \
--data-raw '{
    "type": "sqlDataRequest",
    "user": {"id": "2", "userName": "jacob.test@example.com"},
    "dataProviders": ["EXT-PROVIDER"],
    "data_request": {
        "type": "sqlDataRequest",
        "query": "SELECT * FROM PERSONEN LIMIT 10",
        "algorithm": "aggregate",
        "options": {"graph": false, "aggregate": true},
        "requestMetadata": {}
    }
}'
```