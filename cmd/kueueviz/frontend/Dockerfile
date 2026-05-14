# Build stage runs on the build host's native architecture. Vite v8 pulls
# Rolldown and lightningcss native bindings that are not published for
# linux/ppc64le or linux/s390x; running the build on BUILDPLATFORM keeps the
# bundler off those archs entirely. The build output is static JS/CSS, so it
# is architecture-agnostic.
FROM --platform=${BUILDPLATFORM} node:26-slim AS builder

WORKDIR /app

COPY package*.json ./

RUN npm install

COPY . .

RUN npm run build

# Install `serve` into a self-contained prefix on BUILDPLATFORM. `serve` is
# pure JavaScript, so its node_modules tree can be copied verbatim into the
# target-arch runtime image without re-running npm there.
RUN npm install --prefix /opt/serve serve

# Runtime stage uses TARGETPLATFORM so the produced image carries correct
# arch metadata for the multi-arch manifest. It contains no RUN steps, so
# building it for ppc64le/s390x does not require QEMU binfmt_misc on the
# build host — only file copies happen.
FROM --platform=${TARGETPLATFORM} node:26-slim AS runtime

WORKDIR /app

COPY --from=builder /opt/serve/node_modules /opt/serve/node_modules
COPY --from=builder /app/build ./build

EXPOSE 8080

# Invoke serve via node directly to avoid relying on shebang resolution.
# env.js is expected to be mounted via ConfigMap at /app/build/env.js
# when running in Kubernetes.
ENTRYPOINT ["node", "/opt/serve/node_modules/serve/build/main.js", "-s", "build", "-l", "tcp://0.0.0.0:8080"]
