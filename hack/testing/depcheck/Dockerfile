FROM node:26-alpine@sha256:2d984a15c9b54fd0aeb608b8e0d0d83529eb34d2966db27a1fb4f1edc3d298a3

# Set working directory
WORKDIR /app

# Copy package files
COPY package*.json ./

# Skip downloading the binary since this is just for dependency check
ENV CYPRESS_INSTALL_BINARY=0

# Install ALL dependencies (including dev dependencies needed for build)
RUN npm install

# Copy source files
COPY . .

CMD ["npm", "run", "check-unused"]
