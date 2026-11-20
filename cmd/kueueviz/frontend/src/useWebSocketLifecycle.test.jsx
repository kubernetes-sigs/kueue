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
import { act, renderHook } from '@testing-library/react';

vi.mock('./utils/urlHelper', () => ({
  buildWebSocketUrl: vi.fn((path) => `ws://localhost${path}`),
}));

vi.mock('./AuthContext', () => ({
  useAuth: () => ({ token: null }),
}));

import useWebSocket from './useWebSocket';

// FakeWebSocket records every socket the hook opens so tests can deliver
// messages to a specific one.
class FakeWebSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static instances = [];

  constructor(url) {
    this.url = url;
    this.readyState = FakeWebSocket.OPEN;
    FakeWebSocket.instances.push(this);
  }

  close() {}

  send(data) {
    this.onmessage?.({ data: JSON.stringify(data) });
  }
}

beforeEach(() => {
  FakeWebSocket.instances = [];
  vi.stubGlobal('WebSocket', FakeWebSocket);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('useWebSocket', () => {
  it('clears the previous data when the URL changes', () => {
    const { result, rerender } = renderHook(({ url }) => useWebSocket(url), {
      initialProps: { url: '/ws/workload/ns-1/wl-1' },
    });
    act(() => FakeWebSocket.instances[0].send({ name: 'wl-1' }));
    expect(result.current.data).toEqual({ name: 'wl-1' });

    rerender({ url: '/ws/workload/ns-1/wl-2' });

    expect(result.current.data).toBeNull();
  });

  it('never renders the previous data for the new URL', () => {
    // Record every render, including the one before effects run.
    const renders = [];
    const { rerender } = renderHook(({ url }) => {
      const result = useWebSocket(url);
      renders.push({ url, data: result.data });
      return result;
    }, { initialProps: { url: '/ws/workload/ns-1/wl-1' } });
    act(() => FakeWebSocket.instances[0].send({ name: 'wl-1' }));

    rerender({ url: '/ws/workload/ns-1/wl-2' });

    const staleRenders = renders.filter((r) => r.url.endsWith('wl-2') && r.data !== null);
    expect(staleRenders).toEqual([]);
  });

  it('ignores messages from the socket of the previous URL', () => {
    const { result, rerender } = renderHook(({ url }) => useWebSocket(url), {
      initialProps: { url: '/ws/workload/ns-1/wl-1' },
    });
    rerender({ url: '/ws/workload/ns-1/wl-2' });
    const [oldSocket, newSocket] = FakeWebSocket.instances;

    act(() => oldSocket.send({ name: 'wl-1' }));
    expect(result.current.data).toBeNull();

    act(() => newSocket.send({ name: 'wl-2' }));
    expect(result.current.data).toEqual({ name: 'wl-2' });
  });
});
