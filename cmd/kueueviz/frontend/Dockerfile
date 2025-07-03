# Build stage
FROM node:24-slim AS builder

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
FROM node:24-slim AS runtime

WORKDIR /app

# Install serve for production static file serving
RUN npm install -g serve

# Copy only the built files from builder
COPY --from=builder /app/build ./build
COPY start.sh /app/start.sh
RUN chmod +x /app/start.sh

EXPOSE 8080

# Use the start script
ENTRYPOINT ["/app/start.sh"]

