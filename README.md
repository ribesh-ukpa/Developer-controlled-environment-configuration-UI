# Dev Env Config

A deliberately lightweight self-service UI that lets developers edit
**ConfigMaps** and **Secrets** in **development** Kubernetes namespaces — so they
no longer have to ping DevOps for every change.

- Single static Go binary, embedded vanilla-JS UI (no build step, no node_modules).
- Talks to the cluster via an **in-cluster ServiceAccount** whose RBAC is scoped
  to `configmaps`/`secrets` and bound **only** into the namespaces you allow.
- **Basic auth** gate + a self-reported "your name" field stamped into an audit log.
- Diff-confirmation before any write.

> ⚠️ **Development namespaces only.** Do not bind the RBAC into staging/prod.
> The namespace allow-list is enforced twice: by Kubernetes RBAC *and* by the app.

## Security model (read this)

| Layer | Control |
|-------|---------|
| What the app *can* touch | `ClusterRole` → `get/list/update/patch` on `configmaps`/`secrets` only. No `create`, no `delete`. |
| Where it can touch it | `RoleBinding` per allowed namespace + app-side `ALLOWED_NAMESPACES` allow-list. |
| Who can use the UI | Shared basic-auth credential from a Secret. |
| Network exposure | `ClusterIP` only — reach via `port-forward` or an internal Ingress you add. |
| Accountability | Every write emits a JSON audit line to stdout (captured by cluster logging). |

**Known limitation of shared-secret auth:** the audit log's `devName` is
self-reported and therefore spoofable. It's useful for "who probably did this"
but is **not** proof of identity. If you need real attribution, front the
Service with `oauth2-proxy`/SSO — the audit plumbing already records whatever
identity you wire in.

## Configuration (env vars)

| Var | Default | Notes |
|-----|---------|-------|
| `APP_PORT` | `8080` | |
| `ALLOWED_NAMESPACES` | `dev` | Comma-separated. **Must** match the RoleBindings. |
| `ALLOW_SECRETS` | `true` | Set `false` to disable the Secrets tab entirely. |
| `BASIC_AUTH_USER` | `developer` | From the auth Secret. |
| `BASIC_AUTH_PASS` | _(required)_ | From the auth Secret. App refuses to start if unset. |

## How it connects to the cluster

The same binary works two ways and auto-detects which:

1. **In-cluster** (production) — uses the Pod's mounted ServiceAccount token.
   RBAC in `deploy/rbac.yaml` bounds what it can do.
2. **Kubeconfig** (local / Docker testing) — falls back to `$KUBECONFIG`
   (else `~/.kube/config`) when no in-cluster token is present.

> ⚠️ In kubeconfig mode the app acts with **your** credentials, so the
> `deploy/rbac.yaml` limits do **not** apply — only the in-app
> `ALLOWED_NAMESPACES` / `ALLOW_SECRETS` guards do. Use it against a dev/local
> cluster only.

## Local testing in Docker

Point it at your current kubeconfig context (needs a `dev` namespace with at
least one ConfigMap to see anything):

```sh
docker compose up --build
# open http://localhost:8080   (developer / test-password)
```

Or plain `docker run`:

```sh
docker build -t dev-env-config:test .
docker run --rm -p 8080:8080 \
  -e BASIC_AUTH_PASS=test-password \
  -e ALLOWED_NAMESPACES=dev \
  -e KUBECONFIG=/kube/config \
  -v "$HOME/.kube/config:/kube/config:ro" \
  dev-env-config:test
```

**Local-cluster networking gotcha:** if your kubeconfig server is `127.0.0.1`
(Docker Desktop / kind / minikube), the container can't reach host loopback.
- Docker Desktop: change the kubeconfig server to `https://host.docker.internal:6443`.
- Linux: add `--network host` (and drop `-p 8080:8080`).

Zero-friction alternative (no Docker), if you have Go installed:

```sh
BASIC_AUTH_PASS=test-password ALLOWED_NAMESPACES=dev go run .
```

## Push the image to GHCR

The deployment pulls from GitHub Container Registry
(`ghcr.io/ribesh-ukpa/developer-controlled-environment-configuration`).

> 🔐 Use a GitHub **Personal Access Token (classic)** with the `write:packages`
> scope as the registry password. **Never** paste a real token into a shell that
> records history, a file, or a chat — pass it via `--password-stdin` and treat
> any leaked token as compromised (revoke + reissue immediately).

```sh
# 1. Log in to GHCR (token piped from an env var, not typed inline)
export GHCR_PAT=<your-token>          # do NOT commit or echo this
echo "$GHCR_PAT" | docker login ghcr.io -u ribesh-ukpa --password-stdin

# 2. Build, tag and push
docker build -t developer-controlled-environment-configuration-ui-dev-env-config:latest .
docker tag developer-controlled-environment-configuration-ui-dev-env-config:latest \
  ghcr.io/ribesh-ukpa/developer-controlled-environment-configuration:latest
docker push ghcr.io/ribesh-ukpa/developer-controlled-environment-configuration:latest
```

If the package is private, the cluster needs a pull secret (already referenced
as `ghcr-secret` in `deploy/deployment.yaml`):

```sh
kubectl create secret docker-registry ghcr-secret \
  --docker-server=ghcr.io \
  --docker-username=ribesh-ukpa \
  --docker-password="$GHCR_PAT" \
  --docker-email=your@email.com \
  --namespace=dev
```

> The pull secret is wired into the Deployment declaratively. If you ever add it
> to a Deployment that lacks it, you can patch instead of re-applying:
> ```sh
> kubectl patch deployment dev-env-config -n dev \
>   -p '{"spec":{"template":{"spec":{"imagePullSecrets":[{"name":"ghcr-secret"}]}}}}'
> ```

## Deploy

```sh
# 1. Build & push the image to GHCR — see "Push the image to GHCR" above,
#    and create the ghcr-secret pull secret if the package is private.

# 2. Create the basic-auth credential (do NOT commit it)
kubectl -n dev create secret generic dev-env-config-auth \
  --from-literal=username=developer \
  --from-literal=password="$(openssl rand -base64 24)"

# 3. Apply RBAC + workload
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/deployment.yaml
kubectl apply -f deploy/service.yaml

# 4. Reach it
# --address 0.0.0.0 is required when the cluster runs on a remote VM so that
# the port-forward binds on all interfaces, not just loopback. Without it the
# port is only reachable from inside the VM (localhost) and browsers on other
# machines will time out.
kubectl -n dev port-forward --address 0.0.0.0 svc/dev-env-config 8080:80
# open http://<VM-IP>:8080  (or http://localhost:8080 if running locally)
```

### Adding another dev namespace

1. Add it to `ALLOWED_NAMESPACES` in `deploy/deployment.yaml`.
2. Copy the `RoleBinding` block in `deploy/rbac.yaml` for that namespace.

## API

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/api/namespaces` | Allowed namespaces + whether secrets are enabled |
| `GET` | `/api/resources/{ns}/{kind}` | List names (`kind` = `configmaps`\|`secrets`) |
| `GET` | `/api/resources/{ns}/{kind}/{name}` | Get key/value data |
| `PUT` | `/api/resources/{ns}/{kind}/{name}` | Replace data (body: `{data, devName}`) |
| `GET` | `/healthz` | Unauthenticated health probe |

## Audit log

Each write prints one line to stdout, e.g.:

```
AUDIT {"time":"2026-06-17T09:00:00Z","devName":"Ribesh","remoteAddr":"10.0.0.5:1234","action":"update","namespace":"dev","kind":"configmaps","name":"api-config","changedKeys":["LOG_LEVEL"]}
```

Ship it to your existing log aggregation (Loki/ELK/Cloud Logging) for retention.

## Notes & by-design limitations

- **Two connection modes.** In-cluster ServiceAccount for production; kubeconfig
  fallback for local/Docker testing (see above). In production, deploy as a Pod
  so the ServiceAccount + RBAC are the boundary — don't ship a kubeconfig.
- **Replace semantics.** A save replaces the resource's entire `data` map with
  what's in the editor (keys removed in the UI are removed on the cluster). The
  diff dialog shows added (`+`) / removed (`-`) / changed (`~`) keys first.
- **Opaque secrets only** are listed (service-account tokens etc. are hidden).
