// @vitest-environment jsdom
/*
Copyright The Kubernetes Authors.

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

import { afterEach, beforeEach, describe, it, expect, vi } from 'vitest';
import { act, cleanup, renderHook } from '@testing-library/react';
import useWebSocket, { parseWebSocketMessage } from './useWebSocket';
import { useAuth } from './AuthContext';

vi.mock('./utils/urlHelper', () => ({
  buildWebSocketUrl: (path) => `ws://backend${path}`,
}));

vi.mock('./AuthContext', () => ({
  useAuth: vi.fn(),
}));

// ---------------------------------------------------------------------------
// parseWebSocketMessage
// ---------------------------------------------------------------------------
describe('parseWebSocketMessage', () => {
  it('parses valid JSON object and returns data with no error', () => {
    const result = parseWebSocketMessage('{"key":"value"}');
    expect(result.data).toEqual({ key: 'value' });
    expect(result.error).toBeNull();
  });

  it('parses a JSON array', () => {
    const result = parseWebSocketMessage('[1, 2, 3]');
    expect(result.data).toEqual([1, 2, 3]);
    expect(result.error).toBeNull();
  });

  it('parses nested JSON objects', () => {
    const input = JSON.stringify({
      metadata: { name: 'test-workload', namespace: 'default' },
      spec: { queueName: 'test-queue' },
    });
    const result = parseWebSocketMessage(input);
    expect(result.data.metadata.name).toBe('test-workload');
    expect(result.error).toBeNull();
  });

  it('returns error for plain text', () => {
    const result = parseWebSocketMessage('hello world');
    expect(result.data).toBeNull();
    expect(result.error).toBe('Received malformed data from the server.');
  });

  it('returns error for empty string', () => {
    const result = parseWebSocketMessage('');
    expect(result.data).toBeNull();
    expect(result.error).toBe('Received malformed data from the server.');
  });

  it('returns error for malformed JSON', () => {
    const result = parseWebSocketMessage('{key: value}');
    expect(result.data).toBeNull();
    expect(result.error).toBe('Received malformed data from the server.');
  });

  it('returns error for truncated JSON', () => {
    const result = parseWebSocketMessage('{"key": "val');
    expect(result.data).toBeNull();
    expect(result.error).toBe('Received malformed data from the server.');
  });

  it('returns error for undefined input', () => {
    const result = parseWebSocketMessage(undefined);
    expect(result.data).toBeNull();
    expect(result.error).toBe('Received malformed data from the server.');
  });

  it('returns error for null input', () => {
    const result = parseWebSocketMessage(null);
    expect(result.data).toBeNull();
    expect(result.error).toBe('Received malformed data from the server.');
  });

  it('returns error for numeric input', () => {
    const result = parseWebSocketMessage(12345);
    expect(result.data).toBeNull();
    expect(result.error).toBe('Received malformed data from the server.');
  });
});

// A minimal WebSocket double that records its instances and lets the tests
// drive the server side of the connection.
class FakeWebSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSED = 3;
  static instances = [];

  constructor(url, protocols) {
    this.url = url;
    this.protocols = protocols;
    this.readyState = FakeWebSocket.CONNECTING;
    this.close = vi.fn();
    FakeWebSocket.instances.push(this);
  }

  open() {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen();
  }

  message(data) {
    if (this.readyState !== FakeWebSocket.OPEN) {
      throw new Error('FakeWebSocket: messages can only be delivered on an open socket');
    }
    this.onmessage({ data });
  }

  // The handshake fails: the error is raised while the socket is connecting.
  failHandshake() {
    this.onerror(new Event('error'));
  }

  // An established connection fails: browsers report the socket as closed.
  fail() {
    this.readyState = FakeWebSocket.CLOSED;
    this.onerror(new Event('error'));
  }

  closeWith(code, reason = '') {
    this.onclose({ code, reason });
  }
}

const lastSocket = () => FakeWebSocket.instances.at(-1);

beforeEach(() => {
  FakeWebSocket.instances = [];
  vi.stubGlobal('WebSocket', FakeWebSocket);
  vi.mocked(useAuth).mockReturnValue({ token: null });
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

describe('useWebSocket', () => {
  it('connects to the backend URL with the base subprotocol only when logged out', () => {
    renderHook(() => useWebSocket('/ws/workloads'));

    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(lastSocket().url).toBe('ws://backend/ws/workloads');
    expect(lastSocket().protocols).toEqual(['kueueviz.v1']);
  });

  it('passes the token as a URL-safe base64 subprotocol', () => {
    // btoa(">>>????") is "Pj4+Pz8/Pw==". "+" is a valid subprotocol character
    // but is replaced for URL-safe encoding, "/" is replaced and the "=" padding
    // is stripped.
    vi.mocked(useAuth).mockReturnValue({ token: '>>>????' });

    renderHook(() => useWebSocket('/ws/workloads'));

    expect(lastSocket().protocols).toEqual(['kueueviz.v1', 'kueueviz.auth.Pj4-Pz8_Pw']);
  });

  it('starts without data or error', () => {
    const { result } = renderHook(() => useWebSocket('/ws/workloads'));

    expect(result.current).toEqual({ data: null, error: null });
  });

  it('exposes the parsed messages as data', () => {
    const { result } = renderHook(() => useWebSocket('/ws/workloads'));

    act(() => lastSocket().open());
    act(() => lastSocket().message('{"workloads":[1]}'));

    expect(result.current).toEqual({ data: { workloads: [1] }, error: null });
  });

  it('keeps the latest message', () => {
    const { result } = renderHook(() => useWebSocket('/ws/workloads'));

    act(() => lastSocket().open());
    act(() => lastSocket().message('{"n":1}'));
    act(() => lastSocket().message('{"n":2}'));

    expect(result.current.data).toEqual({ n: 2 });
  });

  it('reports malformed messages without exposing data', () => {
    const { result } = renderHook(() => useWebSocket('/ws/workloads'));

    act(() => lastSocket().open());
    act(() => lastSocket().message('not json'));

    expect(result.current).toEqual({ data: null, error: 'Received malformed data from the server.' });
  });

  it('keeps delivering data after a malformed message', () => {
    const { result } = renderHook(() => useWebSocket('/ws/workloads'));

    act(() => lastSocket().open());
    act(() => lastSocket().message('not json'));
    act(() => lastSocket().message('{"n":1}'));

    expect(result.current.data).toEqual({ n: 1 });
  });

  it('clears an error when the connection is opened again', () => {
    const { result, rerender } = renderHook(({ url }) => useWebSocket(url), {
      initialProps: { url: '/ws/a' },
    });
    act(() => lastSocket().open());
    act(() => lastSocket().message('not json'));
    expect(result.current.error).toBe('Received malformed data from the server.');

    rerender({ url: '/ws/b' });
    act(() => lastSocket().open());

    expect(FakeWebSocket.instances).toHaveLength(2);
    expect(result.current.error).toBeNull();
  });

  it('reports a failure to connect and closes the socket', () => {
    const { result } = renderHook(() => useWebSocket('/ws/workloads'));

    act(() => lastSocket().failHandshake());

    expect(result.current.error).toBe('Failed to connect to WebSocket.');
    expect(lastSocket().close).toHaveBeenCalledWith(1000);
  });

  it('reports a failure of an established connection', () => {
    const { result } = renderHook(() => useWebSocket('/ws/workloads'));

    act(() => lastSocket().open());
    act(() => lastSocket().fail());

    expect(result.current.error).toBe('WebSocket connection failed.');
  });

  it('does not overwrite the error of a handshake failure with the close that follows it', () => {
    const { result } = renderHook(() => useWebSocket('/ws/workloads'));

    act(() => lastSocket().failHandshake());
    act(() => lastSocket().closeWith(1006));

    expect(result.current.error).toBe('Failed to connect to WebSocket.');
  });

  it('does not overwrite the error of a connection failure with the close that follows it', () => {
    const { result } = renderHook(() => useWebSocket('/ws/workloads'));

    act(() => lastSocket().open());
    act(() => lastSocket().fail());
    act(() => lastSocket().closeWith(1006));

    expect(result.current.error).toBe('WebSocket connection failed.');
  });

  it.each([1000, 1001])('treats close code %i as a normal closure', (code) => {
    const { result } = renderHook(() => useWebSocket('/ws/workloads'));

    act(() => lastSocket().closeWith(code));

    expect(result.current.error).toBeNull();
  });

  it.each([
    [1008, undefined, 'WebSocket connection closed: Token expired or revoked. Please log in again.'],
    [4000, undefined, 'WebSocket connection closed: Bad Request.'],
    [4001, undefined, 'WebSocket connection closed: Unauthorized.'],
    [4002, undefined, 'WebSocket connection closed: Service Unavailable.'],
    [4003, undefined, 'WebSocket connection closed: Forbidden.'],
    [4000, 'namespace is required', 'namespace is required'],
    [4001, 'invalid token', 'invalid token'],
    [4002, 'backend restarting', 'backend restarting'],
    [4003, 'no access to namespace', 'no access to namespace'],
    [1006, undefined, 'WebSocket connection closed unexpectedly (code: 1006). Please refresh the page.'],
  ])('maps close code %i (reason %j) to an error message', (code, reason, expected) => {
    const { result } = renderHook(() => useWebSocket('/ws/workloads'));

    act(() => lastSocket().closeWith(code, reason));

    expect(result.current.error).toBe(expected);
  });

  it('closes the socket on unmount', () => {
    const { unmount } = renderHook(() => useWebSocket('/ws/workloads'));
    const socket = lastSocket();

    unmount();

    expect(socket.close).toHaveBeenCalledWith(1000);
  });

  it('reconnects when the URL changes', () => {
    const { rerender } = renderHook(({ url }) => useWebSocket(url), { initialProps: { url: '/ws/a' } });
    const first = lastSocket();

    rerender({ url: '/ws/b' });

    expect(first.close).toHaveBeenCalledWith(1000);
    expect(FakeWebSocket.instances).toHaveLength(2);
    expect(lastSocket().url).toBe('ws://backend/ws/b');
  });

  it('reconnects when the token changes', () => {
    vi.mocked(useAuth).mockReturnValue({ token: 'old' });
    const { rerender } = renderHook(() => useWebSocket('/ws/a'));
    const first = lastSocket();
    vi.mocked(useAuth).mockReturnValue({ token: 'new' });

    rerender();

    expect(first.close).toHaveBeenCalledWith(1000);
    expect(FakeWebSocket.instances).toHaveLength(2);
    expect(lastSocket().protocols).toEqual(['kueueviz.v1', 'kueueviz.auth.bmV3']);
  });
});
