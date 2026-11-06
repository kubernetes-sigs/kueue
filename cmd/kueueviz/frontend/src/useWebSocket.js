/*
Copyright 2024 The Kubernetes Authors.

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

import { useEffect, useState } from 'react';
import { buildWebSocketUrl } from './utils/urlHelper';
import { useAuth } from './AuthContext';

const WS_CLOSE_NORMAL = 1000;
const WS_CLOSE_GOING_AWAY = 1001;
const WS_CLOSE_POLICY_VIOLATION = 1008;

// Custom WebSocket close codes for granular error reporting
const WS_CLOSE_BAD_REQUEST = 4000;
const WS_CLOSE_UNAUTHORIZED = 4001;
const WS_CLOSE_SERVICE_UNAVAILABLE = 4002;
const WS_CLOSE_FORBIDDEN = 4003;

const WS_BASE_PROTOCOL = 'kueueviz.v1';
const WS_TOKEN_PROTOCOL_PREFIX = 'kueueviz.auth.';

// WebSocket subprotocol values must be valid RFC 6455 tokens.
const encodeTokenForProtocol = (token) =>
  btoa(token).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '');

/**
 * Parses a raw WebSocket message string as JSON.
 * Returns { data, error } so callers can handle failures gracefully.
 */
export const parseWebSocketMessage = (raw) => {
  try {
    if (typeof raw !== 'string') {
      throw new TypeError('expected string');
    }
    return { data: JSON.parse(raw), error: null };
  } catch {
    return { data: null, error: 'Received malformed data from the server.' };
  }
};

const useWebSocket = (url) => {
  const { token } = useAuth();
  const fullUrl = buildWebSocketUrl(url);
  // Received data is keyed to its connection. When the URL or token changes,
  // the old data is dropped in the same render, before anything is painted.
  const connectionKey = `${token || ''}@${fullUrl}`;
  const [received, setReceived] = useState({ key: connectionKey, data: null, error: null });
  const isCurrent = received.key === connectionKey;
  const data = isCurrent ? received.data : null;
  const error = isCurrent ? received.error : null;

  useEffect(() => {
    // Ignore late events from the previous connection's socket.
    let active = true;
    const update = (patch) => {
      if (!active) {
        return;
      }
      setReceived((prev) => ({
        ...(prev.key === connectionKey ? prev : { data: null, error: null }),
        key: connectionKey,
        ...patch,
      }));
    };

    const protocols = [WS_BASE_PROTOCOL];
    if (token) {
      protocols.push(`${WS_TOKEN_PROTOCOL_PREFIX}${encodeTokenForProtocol(token)}`);
    }
    const ws = new WebSocket(fullUrl, protocols);
    let handledErrorEvent = false;

    ws.onopen = () => {
      if (!active) {
        return;
      }
      handledErrorEvent = false;
      update({ error: null });
    };

    ws.onmessage = (event) => {
      if (!active) {
        return;
      }
      const result = parseWebSocketMessage(event.data);
      if (result.error) {
        update({ error: result.error });
      } else {
        update({ data: result.data });
      }
    };

    ws.onerror = (err) => {
      if (!active) {
        return;
      }
      handledErrorEvent = true;
      if (ws.readyState === WebSocket.CONNECTING) {
        update({ error: 'Failed to connect to WebSocket.' });
      } else {
        update({ error: 'WebSocket connection failed.' });
      }
      ws.close(WS_CLOSE_NORMAL);
    };

    ws.onclose = (event) => {
      if (!active || handledErrorEvent) {
        return;
      }
      switch (event.code) {
        case WS_CLOSE_NORMAL:
        case WS_CLOSE_GOING_AWAY:
          // Normal closures, no error needed.
          break;
        case WS_CLOSE_POLICY_VIOLATION:
          update({ error: 'WebSocket connection closed: Token expired or revoked. Please log in again.' });
          break;
        case WS_CLOSE_BAD_REQUEST:
          update({ error: event.reason || 'WebSocket connection closed: Bad Request.' });
          break;
        case WS_CLOSE_UNAUTHORIZED:
          update({ error: event.reason || 'WebSocket connection closed: Unauthorized.' });
          break;
        case WS_CLOSE_SERVICE_UNAVAILABLE:
          update({ error: event.reason || 'WebSocket connection closed: Service Unavailable.' });
          break;
        case WS_CLOSE_FORBIDDEN:
          update({ error: event.reason || 'WebSocket connection closed: Forbidden.' });
          break;
        default:
          update({ error: `WebSocket connection closed unexpectedly (code: ${event.code}). Please refresh the page.` });
          break;
      }
    };

    return () => {
      active = false;
      ws.close(WS_CLOSE_NORMAL);
    };
  }, [fullUrl, token, connectionKey]);

  return { data, error };
};

export default useWebSocket;
