# GKE Deployment — EcoFlow Exporter + Prometheus

Deploys the exporter in **REST API mode**.
Metrics are scraped by an existing **kube-prometheus-stack** via `ServiceMonitor`.

> **Why REST and not MQTT?**
> EcoFlow smart plugs (HW52 series) send their data as binary protobuf inside the MQTT
> pdata field. The exporter's protobuf decoder does not yet support this format, so all
> MQTT messages from smart plugs fail with "no params extracted". REST API returns plain
> JSON and works correctly with all device types including smart plugs.

## Prerequisites

- `kubectl` configured and pointing at your GKE cluster
- kube-prometheus-stack (Prometheus Operator) already installed
- EcoFlow developer API keys — get them free from [developer-eu.ecoflow.com](https://developer-eu.ecoflow.com/) (approval takes ~24h)

---

## Step 1 — Get EcoFlow developer API keys

1. Go to [developer-eu.ecoflow.com](https://developer-eu.ecoflow.com/)
2. Click **Become a Developer** → login with your EcoFlow account
3. Wait for approval email (subject: "Approval notice from EcoFlow Developer Platform")
4. Go to the **Security** section → create an **Access Key** and **Secret Key**

Then fill them into [secret.yaml](secret.yaml) **before applying**:

```yaml
stringData:
  access_key: "your-access-key"
  secret_key: "your-secret-key"
```

> **Never commit real credentials to git.** Apply directly with `kubectl apply` or use the imperative form in Step 3.

---

## Step 2 — Device serial numbers (already filled in)

The deployment already has your two smart plug SNs set as friendly names:

```yaml
- name: ECOFLOW_DEVICES_PRETTY_NAMES
  value: '{"HW52XXXXXXXXXXX1":"smart_plug_1","HW52XXXXXXXXXXX2":"smart_plug_2"}'
```

REST API auto-discovers all devices linked to your account, so SNs are optional —
they just give nicer `device` label values in Prometheus instead of the raw serial numbers.
Edit [deployment.yaml](k8s/deployment.yaml) to change the friendly names if you want.

---

## Step 3 — Check the ServiceMonitor selector

Your Prometheus CR only picks up ServiceMonitors that match its `serviceMonitorSelector`.
Find the right label:

```bash
kubectl get prometheus -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}: {.spec.serviceMonitorSelector}{"\n"}{end}'
```

Edit [servicemonitor.yaml](servicemonitor.yaml) and update the `release:` label to match.
For a default kube-prometheus-stack install it is already `release: kube-prometheus-stack`.

---

## Step 4 — Deploy

```bash
# Create namespace
kubectl apply -f k8s/namespace.yaml

# Create secret (edit the file first, or use the imperative form below)
kubectl apply -f k8s/secret.yaml

# Or imperatively (avoids putting credentials in the file):
kubectl create secret generic ecoflow-credentials \
  --namespace ecoflow \
  --from-literal=access_key='your-access-key' \
  --from-literal=secret_key='your-secret-key'

# Deploy the exporter + service + servicemonitor
kubectl apply -f k8s/deployment.yaml
kubectl apply -f k8s/service.yaml
kubectl apply -f k8s/servicemonitor.yaml
```

Or apply everything at once (after editing the files):

```bash
kubectl apply -f k8s/
```

---

## Step 5 — Verify

```bash
# Check pod is Running
kubectl get pods -n ecoflow

# Check logs (should show MQTT connection + metric publishing)
kubectl logs -n ecoflow -l app=ecoflow-exporter -f

# Confirm metrics endpoint is reachable from inside the cluster
kubectl run -it --rm debug --image=curlimages/curl --restart=Never -- \
  curl http://ecoflow-exporter.ecoflow.svc.cluster.local:2112/metrics

# Check ServiceMonitor was picked up by Prometheus
kubectl get servicemonitor -n ecoflow
```

In Prometheus UI → Status → Targets, you should see `ecoflow-exporter` as an active target.

---

## Grafana Dashboard

Import dashboard **17812** from grafana.com:
Grafana → Dashboards → Import → ID `17812` → select your Prometheus datasource.

---

## Environment variable reference

| Variable | Default | Description |
|---|---|---|
| `EXPORTER_TYPE` | `rest` | Set to `mqtt` |
| `PROMETHEUS_ENABLED` | — | Set to `true` |
| `ECOFLOW_EMAIL` | — | EcoFlow account email |
| `ECOFLOW_PASSWORD` | — | EcoFlow account password |
| `ECOFLOW_DEVICES_PRETTY_NAMES` | — | JSON map `{"SN":"name"}` |
| `ECOFLOW_DEVICES` | — | Comma-separated SNs (alternative to pretty names) |
| `MQTT_PING_INTERVAL_SECONDS` | `20` | MQTT keepalive ping interval |
| `MQTT_STATE_REFRESH_INTERVAL_SECONDS` | `300` | Full state refresh interval |
| `MQTT_DEVICE_OFFLINE_THRESHOLD_SECONDS` | `60` | Seconds before device marked offline |
| `PROMETHEUS_PORT` | `2112` | Metrics HTTP port |
