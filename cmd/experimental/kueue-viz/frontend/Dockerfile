#Build the React app
FROM node:16 AS build

ENV NPM_CONFIG_CACHE=/tmp/.npm-cache
WORKDIR /app
COPY package*.json ./
RUN npm install
COPY . .

# Build the production-ready static files
RUN npm run build
RUN mkdir -p build && touch build/env.js && chmod ugo+rw -R build/ $NPM_CONFIG_CACHE
EXPOSE 8080

# Use react-inject-env to be able to inject env vars at runtime
ENTRYPOINT npx react-inject-env set && npx http-server build



