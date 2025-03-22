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
import { env } from './env'

const websocketURL = env.REACT_APP_WEBSOCKET_URL;

const useWebSocket = (url) => {
  const [data, setData] = useState(null);
  const [error, setError] = useState(null);
  const fullUrl = `${websocketURL}${url}`;
  useEffect(() => {
    const ws = new WebSocket(fullUrl);

    ws.onopen = () => {
      console.log(`Connected to WebSocket: ${fullUrl}`);
    };

    ws.onmessage = (event) => {
      const message = JSON.parse(event.data);
      console.log("WebSocket message received:", message); // Log incoming data
      setData(message);
    };

    ws.onerror = (err) => {
      console.error("WebSocket error:", err);
      setError("WebSocket connection error");
      ws.close();
    };

    ws.onclose = () => {
      console.log("WebSocket connection closed");
    };

    // Clean up WebSocket connection on component unmount
    return () => {
      ws.close();
    };
  }, [url]);

  return { data, error };
};

export default useWebSocket;
