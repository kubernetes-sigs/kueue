FROM node:26-alpine@sha256:dbaa92e5758cbbcf85d65d5403fdb530fe3442cbe8c6dbfb7ef23365450d5070

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
