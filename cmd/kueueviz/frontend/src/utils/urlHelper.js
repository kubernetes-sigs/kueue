/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

import { env } from "../env";

/**
 * Gets the backend WebSocket URL from environment variables
 * @returns {string} The WebSocket URL
 * @throws {Error} If no backend URL is configured
 */
export const getBackendWebSocketUrl = () => {
  const backendUrl = env.REACT_APP_WEBSOCKET_URL || env.VITE_WEBSOCKET_URL;
  if (!backendUrl) {
    throw new Error(
      "Backend URL is not configured. Please set REACT_APP_WEBSOCKET_URL or VITE_WEBSOCKET_URL."
    );
  }
  return backendUrl;
};

/**
 * Converts a WebSocket URL to HTTP/HTTPS URL
 * @param {string} wsUrl - The WebSocket URL
 * @returns {string} The HTTP/HTTPS URL
 */
export const convertWebSocketToHttp = (wsUrl) => {
  return wsUrl.replace("ws://", "http://").replace("wss://", "https://");
};

/**
 * Gets the backend HTTP URL for API calls
 * @returns {string} The HTTP URL
 * @throws {Error} If no backend URL is configured
 */
export const getBackendHttpUrl = () => {
  const wsUrl = getBackendWebSocketUrl();
  return convertWebSocketToHttp(wsUrl);
};

/**
 * Builds a complete WebSocket URL with the given path
 * @param {string} path - The WebSocket path (e.g., '/ws/workloads')
 * @returns {string} The complete WebSocket URL
 */
export const buildWebSocketUrl = (path) => {
  const baseUrl = getBackendWebSocketUrl();
  return `${baseUrl}${path}`;
};

/**
 * Builds a complete API URL with the given path and optional query parameters
 * @param {string} path - The API path (e.g., '/api/workload/my-workload')
 * @param {Object} params - Optional query parameters as key-value pairs
 * @returns {string} The complete API URL
 */
export const buildApiUrl = (path, params = {}) => {
  const baseUrl = getBackendHttpUrl();
  const url = new URL(path, baseUrl);

  Object.entries(params).forEach(([key, value]) => {
    if (value !== null && value !== undefined) {
      url.searchParams.append(key, value);
    }
  });

  return url.toString();
};

/**
 * Builds a resource API URL for YAML format
 * @param {string} resourceType - The resource type (e.g., 'workload', 'clusterqueue')
 * @param {string} resourceName - The resource name
 * @param {Object} options - Optional parameters
 * @param {string} options.namespace - The namespace (for namespaced resources)
 * @param {string} options.output - The output format (currently only 'yaml' is supported)
 * @returns {string} The complete resource API URL
 */
export const buildResourceUrl = (resourceType, resourceName, options = {}) => {
  const { namespace, output } = options;
  const path = `/api/${resourceType}/${resourceName}`;

  const params = {};
  if (namespace) {
    params.namespace = namespace;
  }
  if (output) {
    params.output = output;
  }

  return buildApiUrl(path, params);
};
