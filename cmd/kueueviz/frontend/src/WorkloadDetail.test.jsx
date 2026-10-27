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

import { afterEach, describe, it, expect, vi } from 'vitest';
import '@testing-library/jest-dom/vitest';
import React from 'react';
import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import * as useWebSocketModule from './useWebSocket';

vi.mock('./useWebSocket', () => ({
  default: vi.fn(),
}));

import WorkloadDetail, { compareEvents, formatEventTimestamp, getEventTimestamp } from './WorkloadDetail';

describe('formatEventTimestamp', () => {
  it('formats a valid timestamp in UTC', () => {
    expect(formatEventTimestamp('2026-06-26T10:27:28Z')).toBe('2026-06-26@10:27:28 UTC');
  });

  it.each([
    ['undefined', undefined],
    ['null', null],
    ['an empty string', ''],
    ['an invalid date string', 'not-a-timestamp'],
    ['an out-of-range date', '275760-09-13T00:00:00.001Z'],
  ])('returns N/A for %s', (_description, timestamp) => {
    expect(formatEventTimestamp(timestamp)).toBe('N/A');
  });
});

describe('getEventTimestamp', () => {
  it('prefers firstTimestamp for legacy events', () => {
    expect(getEventTimestamp({
      firstTimestamp: '2026-06-26T10:27:28Z',
      eventTime: '2026-06-26T11:00:00.000000Z',
      lastTimestamp: '2026-06-26T10:30:00Z',
    })).toBe('2026-06-26T10:27:28Z');
  });

  it('falls back to eventTime for modern events', () => {
    expect(getEventTimestamp({
      eventTime: '2026-06-26T11:00:00.000000Z',
    })).toBe('2026-06-26T11:00:00.000000Z');
  });

  it('falls back to lastTimestamp when earlier fields are absent', () => {
    expect(getEventTimestamp({
      lastTimestamp: '2026-06-26T10:30:00Z',
    })).toBe('2026-06-26T10:30:00Z');
  });

  it.each([
    ['undefined event', undefined],
    ['empty event', {}],
  ])('returns empty string for %s', (_description, event) => {
    expect(getEventTimestamp(event)).toBe('');
  });
});

describe('compareEvents', () => {
  it('sorts valid timestamps newest first and invalid timestamps last', () => {
    const events = [
      { metadata: { name: 'invalid' }, reason: 'A', firstTimestamp: 'not-a-timestamp' },
      { metadata: { name: 'older' }, reason: 'C', firstTimestamp: '2026-06-26T10:00:00Z' },
      { metadata: { name: 'missing' }, reason: 'B' },
      { metadata: { name: 'newer' }, reason: 'D', eventTime: '2026-06-26T11:00:00Z' },
    ];

    expect([...events].sort(compareEvents).map((event) => event.metadata.name)).toEqual([
      'newer',
      'older',
      'invalid',
      'missing',
    ]);
  });

  it('uses reason and name to order events with equal timestamps', () => {
    const timestamp = '2026-06-26T10:00:00Z';
    const events = [
      { metadata: { name: 'b' }, reason: 'Same', firstTimestamp: timestamp },
      { metadata: { name: 'a' }, reason: 'Same', firstTimestamp: timestamp },
      { metadata: { name: 'c' }, reason: 'Earlier', firstTimestamp: timestamp },
    ];

    expect([...events].sort(compareEvents).map((event) => event.metadata.name)).toEqual(['c', 'a', 'b']);
  });
});

describe('WorkloadDetail owner reference', () => {
  afterEach(cleanup);
  it('shows the owner reference from metadata', () => {
    // Return the same objects on every render, as the real hook does.
    const events = { data: [], error: null };
    const workload = {
      data: {
        metadata: {
          name: 'wl-1',
          namespace: 'ns-1',
          ownerReferences: [{ apiVersion: 'batch/v1', kind: 'Job', name: 'job-1', uid: 'job-uid-1' }],
        },
        spec: { queueName: 'lq-1' },
        status: {},
        clusterQueueName: 'cq-a',
      },
      error: null,
    };
    vi.mocked(useWebSocketModule.default).mockImplementation((url) =>
      url.endsWith('/events') ? events : workload
    );

    render(
      <MemoryRouter initialEntries={['/workload/ns-1/wl-1']}>
        <Routes>
          <Route path="/workload/:namespace/:workloadName" element={<WorkloadDetail />} />
        </Routes>
      </MemoryRouter>
    );

    expect(screen.getByText('API Version: batch/v1')).toBeInTheDocument();
    expect(screen.getByText('Kind: Job')).toBeInTheDocument();
    expect(screen.getByText('Name: job-1')).toBeInTheDocument();
    expect(screen.getByText('UID: job-uid-1')).toBeInTheDocument();
  });
});
