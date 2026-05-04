# General DYNAMOS issues

See the existing troubleshooting section in the root README for 
RabbitMQ connection issues.

---

## Building and pushing your own images

The Helm charts default to the dynamos1 Docker Hub account. You must 
replace this with your own account before deploying.

### Step 1: Log in to Docker Hub

```bash
docker login
```

### Step 2: Replace the Docker Hub account name

Find all references to the original account (e.g. `jorrit05`) across 
all Helm chart values files:

```bash
grep -r "jorrit05" charts/
```

Replace with your own username in each file, or pass it as a Helm value:

```bash
helm upgrade -i ext-provider charts/ext-provider \
  --set dockerArtifactAccount=your-dockerhub-username \
  --set branchNameTag=main
```

### Step 3: Build and push images

```bash
cd go && make all
cd ../python && make all
```

`make all` builds all service images and pushes them to Docker Hub 
tagged as `your-dockerhub-username/service-name:branch-name`.

---

## Prometheus not accessible from Grafana

If Grafana cannot connect to Prometheus, the data source URL is 
likely configured with the wrong port. Prometheus inside the cluster 
listens on port 80, not 9090. To fix this:

1. In Grafana go to **Connections** > **Data Sources**
2. Select the Prometheus data source
3. Update the URL to:

```
http://prometheus-server.default.svc.cluster.local:80
```

4. Click **Save & Test** — it should return a green confirmation