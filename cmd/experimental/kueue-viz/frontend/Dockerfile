# Build stage
FROM node:23 AS builder

# Set working directory
WORKDIR /app

# Copy package files
COPY package*.json ./

# Install dependencies
RUN npm install --production

# Copy source files
COPY . .

# Build the app
RUN npm run build

# Runtime stage
FROM node:23 AS runtime

WORKDIR /app

# Install serve for production static file serving
RUN npm install -g serve react-inject-env

# Copy only the built files from builder
COPY --from=builder /app/build ./build

EXPOSE 8080

# Use serve instead of http-server for better performance
ENTRYPOINT ["sh", "-c", "npx react-inject-env set && serve -s build -l 8080"]



