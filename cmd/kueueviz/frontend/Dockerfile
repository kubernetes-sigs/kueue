# Build stage
FROM node:25-slim AS builder

# Set working directory
WORKDIR /app

# Copy package files
COPY package*.json ./

# Install ALL dependencies (including dev dependencies needed for build)
RUN npm install

# Copy source files
COPY . .

# Build the app
RUN npm run build

# Runtime stage
FROM node:25-slim AS runtime

WORKDIR /app

# Copy package files and install only production dependencies
COPY package*.json ./
RUN npm install --omit=dev

# Copy only the built files from builder
COPY --from=builder /app/build ./build

EXPOSE 8080

# Serve the built files directly.
# env.js is expected to be mounted via ConfigMap at /app/build/env.js
# when running in Kubernetes.
ENTRYPOINT ["npx", "serve", "-s", "build", "-l", "tcp://0.0.0.0:8080"]
