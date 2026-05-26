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

import { describe, it, expect, vi } from 'vitest';

// Mock modules that depend on browser globals so the pure parsing
// function can be imported in the "node" test environment.
vi.mock('./utils/urlHelper', () => ({
  buildWebSocketUrl: vi.fn((path) => `ws://localhost${path}`),
}));

vi.mock('./AuthContext', () => ({
  useAuth: () => ({ token: null }),
}));

vi.mock('./env', () => ({
  env: {},
}));

import { parseWebSocketMessage } from './useWebSocket';

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
