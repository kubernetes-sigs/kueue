FROM node:26-alpine@sha256:8996bb5821d62a8fd9bb27c1cb07462f3fdce6e47d640c6a881ead0b064ce216

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

RUN npm run check-unused
