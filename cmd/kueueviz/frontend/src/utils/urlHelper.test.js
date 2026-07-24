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

import { describe, it, expect, beforeEach, vi } from 'vitest';

// env.js reads `window['env']` at module load time, which doesn't exist in
// the "node" vitest environment this project uses. Mock the module with a
// plain, mutable object instead of importing the real one, mirroring the
// approach used in useWebSocket.test.js for the same reason.
vi.mock('../env', () => ({ env: {} }));

import { env } from '../env';
import {
  getBackendWebSocketUrl,
  convertWebSocketToHttp,
  getBackendHttpUrl,
  buildWebSocketUrl,
  buildApiUrl,
  buildResourceUrl,
} from './urlHelper';

// Reset the mocked env object before every test so each test starts from a
// clean slate regardless of what a previous test set.
const ENV_KEYS = ['REACT_APP_WEBSOCKET_URL', 'VITE_WEBSOCKET_URL'];

beforeEach(() => {
  ENV_KEYS.forEach((key) => delete env[key]);
});

// ---------------------------------------------------------------------------
// getBackendWebSocketUrl
// ---------------------------------------------------------------------------
describe('getBackendWebSocketUrl', () => {
  it('returns REACT_APP_WEBSOCKET_URL when set', () => {
    env.REACT_APP_WEBSOCKET_URL = 'ws://backend:8080';
    expect(getBackendWebSocketUrl()).toBe('ws://backend:8080');
  });

  it('falls back to VITE_WEBSOCKET_URL when REACT_APP_WEBSOCKET_URL is unset', () => {
    env.VITE_WEBSOCKET_URL = 'ws://vite-backend:8080';
    expect(getBackendWebSocketUrl()).toBe('ws://vite-backend:8080');
  });

  it('prefers REACT_APP_WEBSOCKET_URL when both are set', () => {
    env.REACT_APP_WEBSOCKET_URL = 'ws://react-backend:8080';
    env.VITE_WEBSOCKET_URL = 'ws://vite-backend:8080';
    expect(getBackendWebSocketUrl()).toBe('ws://react-backend:8080');
  });

  it('throws when neither variable is configured', () => {
    expect(() => getBackendWebSocketUrl()).toThrow(
      'Backend URL is not configured. Please set REACT_APP_WEBSOCKET_URL or VITE_WEBSOCKET_URL.'
    );
  });

  it('throws when the configured value is an empty string', () => {
    env.REACT_APP_WEBSOCKET_URL = '';
    expect(() => getBackendWebSocketUrl()).toThrow(/not configured/);
  });
});

// ---------------------------------------------------------------------------
// convertWebSocketToHttp
// ---------------------------------------------------------------------------
describe('convertWebSocketToHttp', () => {
  it('converts ws:// to http://', () => {
    expect(convertWebSocketToHttp('ws://localhost:8080')).toBe('http://localhost:8080');
  });

  it('converts wss:// to https://', () => {
    expect(convertWebSocketToHttp('wss://localhost:8080')).toBe('https://localhost:8080');
  });

  it('leaves already-http URLs unchanged', () => {
    expect(convertWebSocketToHttp('http://localhost:8080')).toBe('http://localhost:8080');
  });

  it('only replaces the first occurrence of the scheme', () => {
    // Guards against accidentally converting a ws:// substring that appears
    // later in the URL (e.g. inside a path or query parameter).
    expect(convertWebSocketToHttp('ws://localhost:8080/path?next=ws://other')).toBe(
      'http://localhost:8080/path?next=ws://other'
    );
  });
});

// ---------------------------------------------------------------------------
// getBackendHttpUrl
// ---------------------------------------------------------------------------
describe('getBackendHttpUrl', () => {
  it('derives an http URL from the configured ws backend URL', () => {
    env.REACT_APP_WEBSOCKET_URL = 'ws://backend:8080';
    expect(getBackendHttpUrl()).toBe('http://backend:8080');
  });

  it('derives an https URL from the configured wss backend URL', () => {
    env.REACT_APP_WEBSOCKET_URL = 'wss://backend:8080';
    expect(getBackendHttpUrl()).toBe('https://backend:8080');
  });

  it('propagates the "not configured" error when no backend URL is set', () => {
    expect(() => getBackendHttpUrl()).toThrow(/not configured/);
  });
});

// ---------------------------------------------------------------------------
// buildWebSocketUrl
// ---------------------------------------------------------------------------
describe('buildWebSocketUrl', () => {
  it('appends the given path to the backend websocket URL', () => {
    env.REACT_APP_WEBSOCKET_URL = 'ws://backend:8080';
    expect(buildWebSocketUrl('/ws/workloads')).toBe('ws://backend:8080/ws/workloads');
  });

  it('does not insert a separator if the path lacks a leading slash', () => {
    env.REACT_APP_WEBSOCKET_URL = 'ws://backend:8080';
    expect(buildWebSocketUrl('ws/workloads')).toBe('ws://backend:8080ws/workloads');
  });

  it('throws when no backend URL is configured', () => {
    expect(() => buildWebSocketUrl('/ws/workloads')).toThrow(/not configured/);
  });
});

// ---------------------------------------------------------------------------
// buildApiUrl
// ---------------------------------------------------------------------------
describe('buildApiUrl', () => {
  beforeEach(() => {
    env.REACT_APP_WEBSOCKET_URL = 'ws://backend:8080';
  });

  it('builds a URL with no query parameters', () => {
    expect(buildApiUrl('/api/workloads')).toBe('http://backend:8080/api/workloads');
  });

  it('appends provided query parameters', () => {
    expect(buildApiUrl('/api/workloads', { namespace: 'team-a' })).toBe(
      'http://backend:8080/api/workloads?namespace=team-a'
    );
  });

  it('appends multiple query parameters', () => {
    const url = buildApiUrl('/api/workloads', { namespace: 'team-a', output: 'yaml' });
    expect(url).toBe('http://backend:8080/api/workloads?namespace=team-a&output=yaml');
  });

  it('omits parameters whose value is null', () => {
    expect(buildApiUrl('/api/workloads', { namespace: null })).toBe(
      'http://backend:8080/api/workloads'
    );
  });

  it('omits parameters whose value is undefined', () => {
    expect(buildApiUrl('/api/workloads', { namespace: undefined })).toBe(
      'http://backend:8080/api/workloads'
    );
  });

  it('keeps parameters whose value is an empty string', () => {
    expect(buildApiUrl('/api/workloads', { namespace: '' })).toBe(
      'http://backend:8080/api/workloads?namespace='
    );
  });

  it('URL-encodes parameter values containing special characters', () => {
    expect(buildApiUrl('/api/workloads', { name: 'my job/1' })).toBe(
      'http://backend:8080/api/workloads?name=my+job%2F1'
    );
  });
});

// ---------------------------------------------------------------------------
// buildResourceUrl
// ---------------------------------------------------------------------------
describe('buildResourceUrl', () => {
  beforeEach(() => {
    env.REACT_APP_WEBSOCKET_URL = 'ws://backend:8080';
  });

  it('builds a URL with just resource type and name', () => {
    expect(buildResourceUrl('workload', 'my-workload')).toBe(
      'http://backend:8080/api/workload/my-workload'
    );
  });

  it('includes namespace when provided', () => {
    expect(buildResourceUrl('workload', 'my-workload', { namespace: 'team-a' })).toBe(
      'http://backend:8080/api/workload/my-workload?namespace=team-a'
    );
  });

  it('includes output format when provided', () => {
    expect(buildResourceUrl('clusterqueue', 'my-cq', { output: 'yaml' })).toBe(
      'http://backend:8080/api/clusterqueue/my-cq?output=yaml'
    );
  });

  it('includes both namespace and output when both are provided', () => {
    const url = buildResourceUrl('workload', 'my-workload', {
      namespace: 'team-a',
      output: 'yaml',
    });
    expect(url).toBe('http://backend:8080/api/workload/my-workload?namespace=team-a&output=yaml');
  });

  it('omits namespace for cluster-scoped resources when not provided', () => {
    expect(buildResourceUrl('clusterqueue', 'my-cq')).toBe(
      'http://backend:8080/api/clusterqueue/my-cq'
    );
  });
});