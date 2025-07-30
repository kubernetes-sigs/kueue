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

const useWebSocket = (url) => {
  const [data, setData] = useState(null);
  const [error, setError] = useState(null);
  const fullUrl = buildWebSocketUrl(url);
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
      // Log environment variables to the console
      console.log(`Backend URL: ${fullUrl}`);

      // Function to get all properties, including inherited ones
      const getAllProperties = (obj) => {
        let props = {};
        for (let prop in obj) {
          props[prop] = obj[prop];
        }
        // Manually add non-enumerable properties
        if (obj.currentTarget) {
          props.currentTarget = {
            url: obj.currentTarget.url, // Extract the url property from the WebSocket object
          };
        }
        return props;
      };
      let errorCause = "";    
      switch (ws.readyState) {
        case WebSocket.CONNECTING:
          errorCause = "Failed to establish connection.";
          break;
        case WebSocket.CLOSED:
          errorCause = "Connection was closed unexpectedly.";
          break;
        case WebSocket.CLOSING:
          errorCause = "Connection is in the process of closing.";
          break;
        case WebSocket.OPEN:
          errorCause = "Error occurred on an open connection.";
          break;
        default:
          errorCause = "Unknown connection state.";
      }
      const errorDetails = errorCause + "\n" + JSON.stringify(getAllProperties(err), null, 2);
      const errorMessage = `Failed to fetch data from WebSocket: ${errorDetails}\n`;
      setError(errorMessage);
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
