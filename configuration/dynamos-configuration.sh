#!/bin/bash

set -e

echo "Performing a deep clean of existing resources..."

STUCK_NAMESPACES=("api-gateway" "core" "orchestrator" "uva" "vu" "ext-provider" "ext-provider-agent")

for ns in "${STUCK_NAMESPACES[@]}"; do
    echo "Killing namespace: $ns"
    if kubectl get ns "$ns" >/dev/null 2>&1; then
        kubectl get namespace "$ns" -o json \
            | jq '.spec.finalizers = []' \
            | kubectl replace --raw "/api/v1/namespaces/$ns/finalize" -f -
        echo -e "\n---"
    fi
done

# Uninstall existing Helm releases to clear metadata
helm uninstall agents orchestrator core namespaces surf api-gateway -n default 2>/dev/null || true
helm uninstall agents -n agents 2>/dev/null || true

# Delete Cluster-wide resources 
kubectl delete clusterrole core-cluster-role promtail job-creator --ignore-not-found
kubectl delete clusterrolebinding core-cluster-role-binding promtail job-creator-uva job-creator-vu job-creator-binding-ext --ignore-not-found
kubectl delete serviceaccount job-creator-ext-provider-agent -n ext-provider-agent --ignore-not-found
kubectl delete rolebinding job-creator-ext-provider-agent -n ext-provider-agent --ignore-not-found
kubectl delete serviceaccount job-creator-uva -n uva --ignore-not-found
kubectl delete serviceaccount job-creator-vu -n vu --ignore-not-found
kubectl delete rolebinding job-creator-uva -n uva --ignore-not-found
kubectl delete rolebinding job-creator-vu -n vu --ignore-not-found
echo "Initiating forced deletion (non-blocking)..."
kubectl delete ns uva vu agents orchestrator core api-gateway ext-provider ext-provider-agent --force --grace-period=0 --ignore-not-found --wait=false

# This ensures that even if they are stuck, they are removed.
for ns in uva vu agents orchestrator core api-gateway ext-provider ext-provider-agent; do
    if kubectl get ns "$ns" >/dev/null 2>&1; then
        echo "Removing finalizers for stuck namespace: $ns"
        kubectl get namespace "$ns" -o json | jq '.spec.finalizers = []' > temp.json
        kubectl replace --raw "/api/v1/namespaces/$ns/finalize" -f temp.json
        rm temp.json
    fi
done

# Give it a few seconds to clear
sleep 5

# 4. Clear Jaeger/Linkerd remnants if they exist
kubectl delete service jaeger-collector-nodeport -n linkerd-jaeger --ignore-not-found

# echo "Cleanup complete. Starting installation..."

# Change this to the path of the DYNAMOS repository on your disk
echo "Setting up paths..."
DYNAMOS_ROOT="${HOME}/DYNAMOS"

# Charts
charts_path="${DYNAMOS_ROOT}/charts"
core_chart="${charts_path}/core"
namespace_chart="${charts_path}/namespaces"
orchestrator_chart="${charts_path}/orchestrator"
agents_chart="${charts_path}/agents"
ttp_chart="${charts_path}/thirdparty"
api_gw_chart="${charts_path}/api-gateway"

# Config
config_path="${DYNAMOS_ROOT}/configuration"
k8s_service_files="${config_path}/k8s_service_files"
etcd_launch_files="${config_path}/etcd_launch_files"

rabbit_definitions_file="${k8s_service_files}/definitions.json"
example_definitions_file="${k8s_service_files}/definitions_example.json"

cp "$example_definitions_file" "$rabbit_definitions_file"
echo "definitions_example.json copied over definitions.json to ensure a clean file"

echo "Generating RabbitMQ password..."
# Create a password for a rabbit user
rabbit_pw=$(openssl rand -hex 16)

# Use the RabbitCtl to make a special hash of that password:
hashed_pw=$($SUDO docker run --rm rabbitmq:3-management rabbitmqctl hash_password $rabbit_pw)
actual_hash=$(echo "$hashed_pw" | tail -n 1)

echo "Replacing tokens..."
cp ${k8s_service_files}/definitions_example.json ${rabbit_definitions_file}


# The Rabbit Hashed password needs to be in definitions.json file, that is the configuration for RabbitMQ
if [[ "$OSTYPE" == "darwin"* ]]; then
    # macOS sed
    sed -i '' "s|%PASSWORD%|${actual_hash}|g" ${rabbit_definitions_file}
else
    # GNU sed
    sed -i "s|%PASSWORD%|${actual_hash}|g" ${rabbit_definitions_file}
fi

echo "Installing namespaces..."

# Install namespaces
helm upgrade -i -f ${namespace_chart}/values.yaml namespaces ${namespace_chart} --set secret.password=${rabbit_pw}

echo "Preparing PVC"

{
    cd ${DYNAMOS_ROOT}/configuration
    ./fill-rabbit-pvc.sh
}

#Install prometheus
echo "Installing Prometheus..."

helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
helm upgrade -i -f "${core_chart}/prometheus-values.yaml" prometheus prometheus-community/prometheus

echo "Patch Prometheus to fix node exporter..."
kubectl patch ds prometheus-prometheus-node-exporter --type "json" -p '[{"op": "remove", "path" : "/spec/template/spec/containers/0/volumeMounts/2/mountPropagation"}]'

echo "Installing NGINX..."
# helm install -f "${core_chart}/ingress-values.yaml" nginx oci://ghcr.io/nginxinc/charts/nginx-ingress -n ingress --version 0.18.0
helm upgrade -i nginx oci://ghcr.io/nginxinc/charts/nginx-ingress \
  --namespace ingress \
  --create-namespace \
  --version 0.18.0 \
  -f "${core_chart}/ingress-values.yaml"

echo "Installing DYNAMOS core..."
helm upgrade -i -f ${core_chart}/values.yaml core ${core_chart} --set hostPath=${HOME}

sleep 3
# Install orchestrator layer
helm upgrade -i -f "${orchestrator_chart}/values.yaml" orchestrator ${orchestrator_chart}

sleep 1

echo "Installing agents layer"
helm upgrade -i -f "${agents_chart}/values.yaml" agents ${agents_chart}

sleep 1

echo "Installing thirdparty layer..."
helm upgrade -i -f "${ttp_chart}/values.yaml" surf ${ttp_chart}

sleep 1

echo "Installing api gateway"
helm upgrade -i -f "${api_gw_chart}/values.yaml" api-gateway ${api_gw_chart}

echo "Finished setting up DYNAMOS"

exit 0
