#!/usr/bin/env bash
# Installs the Track and Docs charts on a throwaway kind cluster behind an Envoy
# front proxy and checks that each /healthz answers 200 THROUGH Envoy: the
# response must carry Envoy's `server` and `x-envoy-upstream-service-time`
# headers and the service's own {"ok":true} body. The cluster is deleted on
# exit, pass or fail.
#
#   make helm-kind                                    # Track built from this checkout
#   TRACK_IMAGE=ghcr.io/gaboracnicolai/talyvor-track:<sha> make helm-kind
#   DOCS_IMAGE=ghcr.io/gaboracnicolai/talyvor-docs:<sha> make helm-kind
#   KEEP_CLUSTER=1 make helm-kind                     # leave the cluster up
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
CLUSTER=${CLUSTER:-talyvor-charts}
HOST_PORT=${HOST_PORT:-18080}
DOCS_IMAGE=${DOCS_IMAGE:-ghcr.io/gaboracnicolai/talyvor-docs:latest}
NS=talyvor

diagnose() {
  echo "::group::cluster state"
  kubectl -n "$NS" get pods,svc,endpoints -o wide || true
  kubectl -n "$NS" describe pods || true
  for app in track docs; do
    kubectl -n "$NS" logs "deploy/$app" --all-containers --tail=100 || true
  done
  kubectl -n "$NS" logs deploy/envoy --tail=50 || true
  echo "::endgroup::"
}

cleanup() {
  local status=$?
  [[ $status -eq 0 ]] || diagnose
  if [[ ${KEEP_CLUSTER:-} == 1 ]]; then
    echo "KEEP_CLUSTER=1: cluster '$CLUSTER' left running (kind delete cluster --name $CLUSTER)"
  else
    kind delete cluster --name "$CLUSTER" || true
  fi
  exit "$status"
}

trap cleanup EXIT
kind create cluster --name "$CLUSTER" --wait 120s --config - <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    extraPortMappings:
      - containerPort: 30080
        hostPort: ${HOST_PORT}
        listenAddress: 127.0.0.1
EOF

# Track: this checkout's image unless TRACK_IMAGE names a published one.
if [[ -n ${TRACK_IMAGE:-} ]]; then
  track_repo=${TRACK_IMAGE%:*} track_tag=${TRACK_IMAGE##*:} track_pull=IfNotPresent
else
  docker build -t talyvor-track:kind "$root"
  kind load docker-image talyvor-track:kind --name "$CLUSTER"
  track_repo=talyvor-track track_tag=kind track_pull=Never
fi

kubectl create namespace "$NS"
pg_password=$(openssl rand -hex 16)
gateway_secret=$(openssl rand -hex 32)
kubectl -n "$NS" create secret generic postgres --from-literal=password="$pg_password"
kubectl -n "$NS" apply -f "$here/kind/postgres.yaml"
kubectl -n "$NS" rollout status deploy/postgres --timeout=180s

db_url() { echo "postgres://talyvor:${pg_password}@postgres.${NS}.svc.cluster.local:5432/$1?sslmode=disable"; }

helm install track "$here/track" -n "$NS" --wait --timeout 5m \
  --set image.repository="$track_repo" \
  --set-string image.tag="$track_tag" \
  --set image.pullPolicy="$track_pull" \
  --set secret.create=true \
  --set-string secret.data.TRACK_DATABASE_URL="$(db_url track)" \
  --set-string secret.data.GATEWAY_AUTH_SECRET="$gateway_secret"

helm install docs "$here/docs" -n "$NS" --wait --timeout 5m \
  --set image.repository="${DOCS_IMAGE%:*}" \
  --set-string image.tag="${DOCS_IMAGE##*:}" \
  --set secret.create=true \
  --set-string secret.data.DOCS_DATABASE_URL="$(db_url docs)" \
  --set-string secret.data.GATEWAY_AUTH_SECRET="$gateway_secret"

kubectl -n "$NS" apply -f "$here/kind/envoy.yaml"
kubectl -n "$NS" rollout status deploy/envoy --timeout=180s

# probe HOST: GET /healthz through Envoy with that Host header.
probe() {
  local host=$1 headers body code
  for _ in $(seq 1 30); do
    headers=$(mktemp) body=$(mktemp)
    code=$(curl -s -D "$headers" -o "$body" -w '%{http_code}' -H "Host: $host" \
      "http://127.0.0.1:${HOST_PORT}/healthz" || true)
    if [[ $code == 200 ]] &&
      grep -qi '^server: envoy' "$headers" &&
      grep -qi '^x-envoy-upstream-service-time:' "$headers" &&
      grep -q '"ok":true' "$body"; then
      echo "PASS ${host}/healthz -> 200 through Envoy: $(cat "$body")"
      rm -f "$headers" "$body"
      return 0
    fi
    rm -f "$headers" "$body"
    sleep 2
  done
  echo "FAIL ${host}/healthz through Envoy: last status ${code:-none}" >&2
  return 1
}

probe track.local
probe docs.local
