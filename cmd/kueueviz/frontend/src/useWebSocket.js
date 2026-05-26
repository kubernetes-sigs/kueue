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

const WS_BASE_PROTOCOL = 'kueueviz.v1';
const WS_TOKEN_PROTOCOL_PREFIX = 'kueueviz.auth.';

// WebSocket subprotocol values must be valid RFC 6455 tokens.
const encodeTokenForProtocol = (token) =>
  btoa(token).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '');

const useWebSocket = (url) => {
  const [data, setData] = useState(null);
  const [error, setError] = useState(null);
  const { token } = useAuth();
  const fullUrl = buildWebSocketUrl(url);

  useEffect(() => {
    const protocols = [WS_BASE_PROTOCOL];
    if (token) {
      protocols.push(`${WS_TOKEN_PROTOCOL_PREFIX}${encodeTokenForProtocol(token)}`);
    }
    const ws = new WebSocket(fullUrl, protocols);

    ws.onopen = () => {
      console.log(`Connected to WebSocket: ${fullUrl}`);
    };

    ws.onmessage = (event) => {
      const message = JSON.parse(event.data);
      setData(message);
    };

    ws.onerror = (err) => {
      console.error('WebSocket error:', err);
      if (ws.readyState === WebSocket.CONNECTING) {
        setError('Failed to connect to WebSocket.');
      } else {
        setError('WebSocket connection failed.');
      }
      ws.close();
    };

    ws.onclose = (event) => {
      console.log('WebSocket connection closed', 'code:', event.code);
    };

    return () => {
      ws.close();
    };
  }, [fullUrl, token]);

  return { data, error };
};

export default useWebSocket;
