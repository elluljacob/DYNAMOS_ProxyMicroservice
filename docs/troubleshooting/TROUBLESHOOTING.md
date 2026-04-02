# DYNAMOS Troubleshooting Guide

This guide documents common issues and debugging commands for the DYNAMOS system.

---

## Table of Contents

1. [Quick Health Check](#quick-health-check)
2. [RabbitMQ Connection Issues](#rabbitmq-connection-issues)
3. [Linkerd Service Mesh Issues](#linkerd-service-mesh-issues)
4. [Pod Debugging](#pod-debugging)
5. [Useful Commands Reference](#useful-commands-reference)

---

## Quick Health Check

### Check all pods status
```bash
kubectl get pods -A | grep -E "orchestrator|api-gateway|rabbit|CrashLoop|Error|NotReady"
```

### Check specific namespace pods
```bash
kubectl get pods -n orchestrator
kubectl get pods -n api-gateway
kubectl get pods -n core
```

### Watch pods in real-time
```bash
watch kubectl get pods -A
```

---

## RabbitMQ Connection Issues

### Symptoms
- Sidecar logs show: `Exception (403) Reason: "username or password not allowed"`
- Sidecar logs show: `Exception (501) Reason: "EOF"` or `i/o timeout`
- Pods in `CrashLoopBackOff` state

### Diagnosis Commands

#### Check sidecar logs
```bash
# Current logs
kubectl logs deployment/api-gateway -n api-gateway -c sidecar --tail=30

# Previous crashed container logs
kubectl logs deployment/api-gateway -n api-gateway -c sidecar --tail=50 --previous
```

#### Check RabbitMQ is running
```bash
kubectl get pods -n core | grep rabbitmq
kubectl logs deployment/rabbitmq -n core -c rabbitmq --tail=50
```

#### Test network connectivity to RabbitMQ
```bash
# From inside a pod
kubectl exec -n orchestrator deployment/orchestrator -c sidecar -- nc -zv rabbitmq.core.svc.cluster.local 5672
```

#### Check RabbitMQ users
```bash
kubectl exec -n core deployment/rabbitmq -c rabbitmq -- rabbitmqctl list_users
```

#### Test authentication
```bash
kubectl exec -n core deployment/rabbitmq -c rabbitmq -- rabbitmqctl authenticate_user normal_user <password>
```

#### Check password in Kubernetes secrets
```bash
# Check what password the services are using
kubectl get secret rabbit -n orchestrator -o jsonpath='{.data.password}' | base64 -d && echo
kubectl get secret rabbit -n api-gateway -o jsonpath='{.data.password}' | base64 -d && echo
```

### Fixing Password Mismatch

#### Option 1: Update RabbitMQ to use the password from secrets
```bash
# Get password from secrets
RABBIT_PW=$(kubectl get secret rabbit -n orchestrator -o jsonpath='{.data.password}' | base64 -d)

# Set password in RabbitMQ
kubectl exec -n core deployment/rabbitmq -c rabbitmq -- rabbitmqctl change_password normal_user "$RABBIT_PW"

# Verify it works
kubectl exec -n core deployment/rabbitmq -c rabbitmq -- rabbitmqctl authenticate_user normal_user "$RABBIT_PW"
```

#### Option 2: Update all secrets to match RabbitMQ (if you know the password)
```bash
# Update secrets in all namespaces (replace <password> with actual password)
PASSWORD_B64=$(echo -n "<password>" | base64)
for ns in orchestrator api-gateway uva vu surf ingress; do
  kubectl patch secret rabbit -n $ns -p "{\"data\":{\"password\":\"$PASSWORD_B64\"}}" 2>/dev/null && echo "Patched $ns"
done
```

#### Option 3: Re-run full configuration (cleanest solution)
```bash
cd ${DYNAMOS_ROOT}/configuration
./dynamos-configuration.sh
```

### After fixing, restart affected deployments
```bash
kubectl rollout restart deployment/api-gateway -n api-gateway
kubectl rollout restart deployment/orchestrator -n orchestrator
```

---

## Linkerd Service Mesh Issues

### Symptoms
- `Exception (501) Reason: "EOF"` with `i/o timeout` (but TCP works)
- `PostStartHookError` on pods
- Certificate expired errors in logs

### Diagnosis Commands

#### Check Linkerd control plane
```bash
kubectl get pods -n linkerd
```

#### Check if Linkerd is injected into pods
```bash
kubectl get pods -n orchestrator -o jsonpath='{range .spec.containers[*]}{.name}{"\n"}{end}'
# If you see "linkerd-proxy", Linkerd is injected
```

#### Check Linkerd certificates
```bash
kubectl get secret -n linkerd linkerd-identity-issuer -o jsonpath='{.data.crt\.pem}' | base64 -d | openssl x509 -noout -dates
```

#### Check for certificate errors in pod
```bash
kubectl describe pod -n api-gateway -l app=api-gateway | grep -A20 "PostStart"
```

### Fixing Linkerd AMQP Issues

AMQP is not HTTP, so Linkerd proxies can interfere with the protocol. Add skip annotations:

#### Add skip-outbound-ports to services connecting TO RabbitMQ
```bash
kubectl patch deployment api-gateway -n api-gateway -p '{"spec":{"template":{"metadata":{"annotations":{"config.linkerd.io/skip-outbound-ports":"5672"}}}}}'
kubectl patch deployment orchestrator -n orchestrator -p '{"spec":{"template":{"metadata":{"annotations":{"config.linkerd.io/skip-outbound-ports":"5672"}}}}}'
kubectl patch deployment policy-enforcer -n orchestrator -p '{"spec":{"template":{"metadata":{"annotations":{"config.linkerd.io/skip-outbound-ports":"5672"}}}}}'
```

#### Add skip-inbound-ports to RabbitMQ
```bash
kubectl patch deployment rabbitmq -n core -p '{"spec":{"template":{"metadata":{"annotations":{"config.linkerd.io/skip-inbound-ports":"5672"}}}}}'
```

### Fixing Expired Linkerd Certificates

#### Restart Linkerd control plane
```bash
kubectl rollout restart deployment/linkerd-identity -n linkerd
kubectl rollout restart deployment/linkerd-destination -n linkerd
kubectl rollout restart deployment/linkerd-proxy-injector -n linkerd

# Wait for rollout
kubectl rollout status deployment/linkerd-identity -n linkerd --timeout=60s
```

#### Delete and recreate affected pods
```bash
kubectl delete pods -n api-gateway -l app=api-gateway
kubectl delete pods -n orchestrator -l app=orchestrator
```

---

## Pod Debugging

### Check pod events
```bash
kubectl describe pod -n <namespace> <pod-name> | grep -A20 "Events:"
```

### Check all containers in a pod
```bash
kubectl get pod <pod-name> -n <namespace> -o jsonpath='{range .spec.containers[*]}{.name}{"\n"}{end}'
```

### Get logs from specific container
```bash
kubectl logs <pod-name> -n <namespace> -c <container-name>
```

### Execute commands in a container
```bash
kubectl exec -n <namespace> <pod-name> -c <container-name> -- <command>
```

### Check environment variables
```bash
kubectl exec -n api-gateway deployment/api-gateway -c sidecar -- env | grep AMQ
```

---

## Useful Commands Reference

### Deployment Management

| Command | Description |
|---------|-------------|
| `kubectl rollout restart deployment/<name> -n <ns>` | Restart a deployment |
| `kubectl rollout status deployment/<name> -n <ns>` | Check rollout status |
| `kubectl rollout undo deployment/<name> -n <ns>` | Rollback deployment |
| `helm upgrade -i -f values.yaml <release> <chart>` | Install/upgrade Helm release |
| `helm uninstall <release>` | Remove Helm release |

### Secrets Management

| Command | Description |
|---------|-------------|
| `kubectl get secrets -A \| grep rabbit` | List all rabbit secrets |
| `kubectl get secret <name> -n <ns> -o yaml` | View secret details |
| `kubectl get secret <name> -n <ns> -o jsonpath='{.data.<key>}' \| base64 -d` | Decode secret value |
| `kubectl patch secret <name> -n <ns> -p '{"data":{"key":"base64value"}}'` | Update secret |

### RabbitMQ Management

| Command | Description |
|---------|-------------|
| `rabbitmqctl list_users` | List all users |
| `rabbitmqctl authenticate_user <user> <pass>` | Test authentication |
| `rabbitmqctl change_password <user> <pass>` | Change user password |
| `rabbitmqctl list_queues` | List all queues |
| `rabbitmqctl list_connections` | List active connections |

Run these inside the RabbitMQ container:
```bash
kubectl exec -n core deployment/rabbitmq -c rabbitmq -- rabbitmqctl <command>
```

### Network Debugging

| Command | Description |
|---------|-------------|
| `nc -zv <host> <port>` | Test TCP connectivity |
| `nslookup <hostname>` | DNS resolution test |
| `curl -v <url>` | HTTP request with verbose output |

### Log Analysis

| Command | Description |
|---------|-------------|
| `kubectl logs -f deployment/<name> -n <ns> -c <container>` | Follow logs |
| `kubectl logs --previous <pod> -n <ns> -c <container>` | Previous container logs |
| `kubectl logs -l app=<label> -n <ns> --all-containers` | Logs from all matching pods |

---

## Common Error Messages and Solutions

| Error | Likely Cause | Solution |
|-------|--------------|----------|
| `Exception (403) Reason: "username or password not allowed"` | Password mismatch between secrets and RabbitMQ | Sync passwords (see above) |
| `Exception (501) Reason: "EOF"` or `i/o timeout` | Linkerd intercepting AMQP traffic | Add skip-outbound-ports annotation |
| `PostStartHookError` | Linkerd certificates expired | Restart Linkerd control plane |
| `panic: runtime error: invalid memory address or nil pointer dereference` | RabbitMQ channel not initialized (code bug) | Update sidecar code with proper error handling |
| `CrashLoopBackOff` | Various - check logs | `kubectl logs --previous` to see crash reason |

---

## DYNAMOS Helper Commands

Source the helper script for convenient commands:
```bash
source ${DYNAMOS_ROOT}/configuration/dynamos-helpers.sh
```

| Function | Description |
|----------|-------------|
| `deploy_core` | Deploy core services (RabbitMQ, etcd, etc.) |
| `deploy_orchestrator` | Deploy orchestrator |
| `deploy_api_gateway` | Deploy API gateway |
| `deploy_all` | Deploy everything |
| `uninstall_all` | Remove all deployments |
| `restart_core` | Restart RabbitMQ |
| `watch_pods` | Watch all pods |
| `redeploy_structurally` | Clean redeploy in correct order |

---

## Preventive Measures

### Make Linkerd skip annotations permanent
Add to your Helm chart templates (e.g., `orchestrator.yaml`):
```yaml
spec:
  template:
    metadata:
      annotations:
        config.linkerd.io/skip-outbound-ports: "5672"
```

### Ensure password consistency
After running `dynamos-configuration.sh`, don't manually change passwords. If you need to redeploy RabbitMQ, re-run the full configuration script to keep everything in sync.

### Monitor Linkerd certificate expiration
Set up alerts for Linkerd certificate expiration, or configure automatic rotation.
