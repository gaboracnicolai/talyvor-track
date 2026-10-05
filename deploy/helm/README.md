# Track and Docs Helm charts

| Chart | Image | Port | Health |
|---|---|---|---|
| `deploy/helm/track` | `ghcr.io/gaboracnicolai/talyvor-track` | 3000 | `/healthz`, `/livez`, `/readyz` |
| `deploy/helm/docs` | `ghcr.io/gaboracnicolai/talyvor-docs` | 4000 | `/healthz`, `/readyz` |

Both expect an external Postgres with the `vector` extension available
(`pgvector/pgvector:pg16` works) and read their secrets from a Secret you create.

## Install

```sh
kubectl create secret generic track-env \
  --from-literal=TRACK_DATABASE_URL='postgres://…/track' \
  --from-literal=GATEWAY_AUTH_SECRET="$(openssl rand -hex 32)"
helm install track deploy/helm/track --set secret.existingSecret=track-env

kubectl create secret generic docs-env \
  --from-literal=DOCS_DATABASE_URL='postgres://…/docs' \
  --from-literal=GATEWAY_AUTH_SECRET='<the same gateway secret>'
helm install docs deploy/helm/docs --set secret.existingSecret=docs-env
```

Track runs `track migrate up` in an init container; Docs applies its migrations
when it boots. Non-secret settings go under `env:` (for example
`--set env.DOCS_TRACK_URL=http://track:3000`). Pin `image.tag` to a commit sha
in production.

## Test on kind

```sh
deploy/helm/kind-test.sh        # or: make helm-kind
```

It creates a throwaway kind cluster, builds Track from this checkout, installs
Postgres, both charts and an Envoy front proxy, and passes only if
`track.local/healthz` and `docs.local/healthz` both answer 200 through Envoy.
The cluster is deleted when it exits. CI runs it in `.github/workflows/charts.yaml`.
