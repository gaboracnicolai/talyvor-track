# syntax=docker/dockerfile:1.6
#
# Two-stage build: compile the SPA on node, serve the static bundle
# from nginx. Final image is ~25 MB and contains no Node runtime.

FROM node:20-alpine AS builder
WORKDIR /app

# Manifests change less often than source — separate copy = better
# layer caching for the install step.
COPY package.json package-lock.json* ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci

COPY . .

# Vite reads VITE_API_URL at build time, so the value bakes into the
# bundle. Override with --build-arg VITE_API_URL=… in the build call
# when targeting a non-default API origin.
ARG VITE_API_URL=
ENV VITE_API_URL=${VITE_API_URL}

RUN npm run build

# ---

FROM nginx:alpine AS runtime

# SPA fallback: every unmatched path serves index.html so React Router
# can pick the route up client-side. /v1 and /mcp are proxied to the
# backend so the frontend can run on a separate host:port without CORS.
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=builder /app/dist /usr/share/nginx/html

EXPOSE 80
HEALTHCHECK --interval=10s --timeout=3s --retries=3 \
    CMD wget -qO- http://localhost/ >/dev/null || exit 1
