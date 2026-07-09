FROM node:26-alpine@sha256:e88a35be04478413b7c71c455cd9865de9b9360e1f43456be5951032d7ac1a66

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
