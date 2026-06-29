FROM node:26-alpine@sha256:725aeba2364a9b16beae49e180d83bd597dbd0b15c47f1f28875c290bfd255b9

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
