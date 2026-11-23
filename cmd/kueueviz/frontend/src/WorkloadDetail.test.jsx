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
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { cleanup, render, screen, within } from '@testing-library/react';
import { mockWebSocket, renderAtRoute } from './testing/helpers';

vi.mock('./useWebSocket', () => ({
  default: vi.fn(),
}));

import WorkloadDetail, { compareEvents, formatEventTimestamp, getEventTimestamp } from './WorkloadDetail';

afterEach(cleanup);

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

const WORKLOAD_URL = '/ws/workload/ns-1/wl-1';
const EVENTS_URL = `${WORKLOAD_URL}/events`;

const workload = (overrides = {}) => ({
  metadata: { name: 'wl-1', namespace: 'ns-1' },
  spec: {
    queueName: 'lq-1',
    priority: 7,
    podSets: [{ count: 2 }, { count: 3 }],
  },
  clusterQueueName: 'cq-1',
  ...overrides,
});

const event = (uid, overrides = {}) => ({
  metadata: { uid },
  type: 'Normal',
  reason: 'Admitted',
  message: `message ${uid}`,
  firstTimestamp: '2026-01-02T03:04:05Z',
  ...overrides,
});

const renderPage = ({ workloadResponse, eventsResponse } = {}) => {
  mockWebSocket({
    [WORKLOAD_URL]: workloadResponse ?? { data: workload(), error: null },
    [EVENTS_URL]: eventsResponse ?? { data: [], error: null },
  });
  return renderAtRoute(<WorkloadDetail />, '/workload/:namespace/:workloadName', '/workload/ns-1/wl-1');
};

describe('WorkloadDetail', () => {
  it('shows the owner reference from metadata', () => {
    renderPage({
      workloadResponse: {
        data: workload({
          metadata: {
            name: 'wl-1',
            namespace: 'ns-1',
            ownerReferences: [{ apiVersion: 'batch/v1', kind: 'Job', name: 'job-1', uid: 'job-uid-1' }],
          },
        }),
        error: null,
      },
    });

    expect(screen.getByText('API Version: batch/v1')).toBeInTheDocument();
    expect(screen.getByText('Kind: Job')).toBeInTheDocument();
    expect(screen.getByText('Name: job-1')).toBeInTheDocument();
    expect(screen.getByText('UID: job-uid-1')).toBeInTheDocument();
  });

  it('shows a spinner until the workload arrives', () => {
    renderPage({ workloadResponse: { data: null, error: null } });

    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('shows the workload error instead of the page', () => {
    renderPage({ workloadResponse: { data: null, error: 'workload failed' } });

    expect(screen.getByText('workload failed')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: /Workload Detail/ })).not.toBeInTheDocument();
  });

  it('renders the queues, priority and pod counts', () => {
    renderPage();

    expect(screen.getByRole('heading', { name: 'Workload Detail: ns-1/wl-1' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'lq-1' })).toHaveAttribute('href', '/local-queue/ns-1/lq-1');
    expect(screen.getByRole('link', { name: 'cq-1' })).toHaveAttribute('href', '/cluster-queue/cq-1');
    expect(screen.getByText(/^Priority:/).parentElement).toHaveTextContent('Priority: 7');
    expect(screen.getByText(/Pod Sets Count:/).parentElement).toHaveTextContent('Pod Sets Count: 2');
    expect(screen.getByText(/Pod Count:/).parentElement).toHaveTextContent('Pod Count: 5');
  });

  it.each([undefined, 'Unknown'])('does not link an unknown cluster queue (%s)', (clusterQueueName) => {
    renderPage({ workloadResponse: { data: workload({ clusterQueueName }), error: null } });

    const queueField = screen.getByText(/Queue Name:/).parentElement;
    expect(queueField).toHaveTextContent('lq-1 → Unknown');
    // Only the local queue is a link, the unknown cluster queue is plain text.
    const links = within(queueField).getAllByRole('link');
    expect(links).toHaveLength(1);
    expect(links[0]).toHaveAttribute('href', '/local-queue/ns-1/lq-1');
  });

  it('shows N/A for fields the workload does not have', () => {
    renderPage({ workloadResponse: { data: { metadata: {}, spec: {} }, error: null } });

    expect(screen.getByText(/^Priority:/).parentElement).toHaveTextContent('Priority: N/A');
    expect(screen.getByText(/Pod Sets Count:/).parentElement).toHaveTextContent('N/A');
    expect(screen.getByText('API Version: N/A')).toBeInTheDocument();
  });

  it('renders an empty event table when there are no events', () => {
    renderPage();

    expect(screen.getByRole('heading', { name: 'Events' })).toBeInTheDocument();
    expect(screen.getAllByRole('row')).toHaveLength(1);
  });

  it('renders events newest first with a UTC timestamp', () => {
    renderPage({
      eventsResponse: {
        data: [
          event('old', { firstTimestamp: '2026-01-01T00:00:00Z' }),
          event('new', { firstTimestamp: '2026-01-03T10:20:30Z', type: 'Warning', reason: 'Evicted' }),
        ],
        error: null,
      },
    });

    const rows = screen.getAllByRole('row').slice(1);
    expect(rows.map((row) => within(row).getAllByRole('cell').map((cell) => cell.textContent))).toEqual([
      ['2026-01-03@10:20:30 UTC', 'Warning', 'Evicted', 'message new'],
      ['2026-01-01@00:00:00 UTC', 'Normal', 'Admitted', 'message old'],
    ]);
  });

  it('shows an events error without hiding the workload', () => {
    renderPage({ eventsResponse: { data: null, error: 'events failed' } });

    expect(screen.getByText('events failed')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Workload Detail: ns-1/wl-1' })).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('ignores event data that is not a list and keeps the events shown before', () => {
    // A new element per render: rerendering the identical element object would
    // let React skip the update and the second response would never be seen.
    const page = () => (
      <MemoryRouter initialEntries={['/workload/ns-1/wl-1']}>
        <Routes>
          <Route path="/workload/:namespace/:workloadName" element={<WorkloadDetail />} />
        </Routes>
      </MemoryRouter>
    );
    const workloadResponse = { data: workload(), error: null };
    mockWebSocket({
      [WORKLOAD_URL]: workloadResponse,
      [EVENTS_URL]: { data: [event('kept')], error: null },
    });
    const { rerender } = render(page());
    expect(screen.getByText('message kept')).toBeInTheDocument();

    mockWebSocket({
      [WORKLOAD_URL]: workloadResponse,
      [EVENTS_URL]: { data: { not: 'a list' }, error: null },
    });
    rerender(page());

    expect(screen.getByText('message kept')).toBeInTheDocument();
    expect(screen.getAllByRole('row')).toHaveLength(2);
  });
});
