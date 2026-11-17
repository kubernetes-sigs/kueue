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
import { cleanup, fireEvent, screen, within } from '@testing-library/react';
import LocalQueues from './LocalQueues';
import useWebSocket from './useWebSocket';
import { mockWebSocket, renderWithRouter } from './testing/helpers';

vi.mock('./useWebSocket', () => ({ default: vi.fn() }));

vi.mock('./ViewYamlButton', () => ({
  default: ({ resourceType, resourceName, namespace }) => (
    <span>{`yaml:${resourceType}/${namespace}/${resourceName}`}</span>
  ),
}));

afterEach(cleanup);

const namespaces = { data: { namespaces: ['team-b', 'team-a'] }, error: null };

const lq = (namespace, name, extra = {}) => ({
  namespace,
  name,
  spec: { clusterQueue: 'cq-1' },
  status: { admittedWorkloads: 1, pendingWorkloads: 2, reservingWorkloads: 3 },
  ...extra,
});

// The first row of a namespace has an extra, spanned namespace cell, so the
// value under a header is located from the end of the row.
const valueUnder = (header, row) => {
  const headers = screen.getAllByRole('columnheader').map((cell) => cell.textContent);
  const cells = within(row).getAllByRole('cell').map((cell) => cell.textContent);
  return cells[cells.length - (headers.length - headers.indexOf(header))];
};

const render = (queues, extra = {}) => {
  mockWebSocket({
    '/ws/namespaces': namespaces,
    '/ws/local-queues': queues,
    ...extra,
  });
  return renderWithRouter(<LocalQueues />);
};

describe('LocalQueues', () => {
  it('shows a spinner until the first message arrives', () => {
    render({ data: null, error: null });

    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it.each([
    ['queues', { data: null, error: 'queues failed' }, undefined, 'queues failed'],
    ['namespaces', { data: [], error: null }, { '/ws/namespaces': { data: null, error: 'namespaces failed' } }, 'namespaces failed'],
  ])('shows the %s error but keeps the filter', (_, queues, extra, message) => {
    render(queues, extra);

    expect(screen.getByText(message)).toBeInTheDocument();
    expect(screen.getByRole('combobox')).toBeInTheDocument();
  });

  it('shows an empty message for all namespaces', () => {
    render({ data: [], error: null });

    expect(screen.getByText('No Local Queues found.')).toBeInTheDocument();
  });

  it('sorts by namespace, then name, and groups rows per namespace', () => {
    render({
      data: [lq('team-b', 'q1'), lq('team-a', 'q2'), lq('team-a', 'q1')],
      error: null,
    });

    const rows = screen.getAllByRole('row').slice(1);
    expect(rows).toHaveLength(3);
    const links = rows.map((row) => within(row).getAllByRole('link')[0]);
    expect(links.map((link) => link.getAttribute('href'))).toEqual([
      '/local-queue/team-a/q1',
      '/local-queue/team-a/q2',
      '/local-queue/team-b/q1',
    ]);
    // The namespace cell spans all queues of the namespace and appears once.
    expect(screen.getByText('team-a').closest('td')).toHaveAttribute('rowspan', '2');
    expect(screen.getAllByText('team-a')).toHaveLength(1);
  });

  it('links each queue to its detail page and cluster queue', () => {
    render({ data: [lq('team-a', 'q1')], error: null });

    const row = screen.getByRole('link', { name: 'q1' }).closest('tr');
    expect(within(row).getByRole('link', { name: 'q1' })).toHaveAttribute('href', '/local-queue/team-a/q1');
    expect(within(row).getByRole('link', { name: 'cq-1' })).toHaveAttribute('href', '/cluster-queue/cq-1');
    expect(valueUnder('Admitted Workloads', row)).toBe('1');
    expect(valueUnder('Pending Workloads', row)).toBe('2');
    expect(valueUnder('Reserving Workloads', row)).toBe('3');
    expect(within(row).getByText('yaml:localqueue/team-a/q1')).toBeInTheDocument();
  });

  it('adds a column per resource with the usage summed over flavors', () => {
    render({
      data: [
        lq('team-a', 'q1', {
          status: {
            flavorsUsage: [
              { name: 'a', resources: [{ name: 'cpu', total: 1.5 }, { name: 'memory', total: 1024 ** 3 }] },
              { name: 'b', resources: [{ name: 'cpu', total: 1 }] },
            ],
          },
        }),
        lq('team-a', 'q2'),
      ],
      error: null,
    });

    const headers = screen.getAllByRole('columnheader').map((header) => header.textContent);
    expect(headers.slice(-3)).toEqual(['cpu', 'memory', 'Actions']);
    const row = screen.getByRole('link', { name: 'q1' }).closest('tr');
    expect(valueUnder('cpu', row)).toBe('2.5');
    expect(valueUnder('memory', row)).toBe('1Gi');
    // A queue without usage shows dashes.
    const other = screen.getByRole('link', { name: 'q2' }).closest('tr');
    expect(within(other).getAllByText('-')).toHaveLength(2);
  });

  it('offers every namespace, sorted, and requests the selected one', () => {
    render({ data: [lq('team-a', 'q1')], error: null });
    fireEvent.mouseDown(screen.getByRole('combobox'));

    const options = screen.getAllByRole('option').map((option) => option.textContent);
    expect(options).toEqual(['All Namespaces', 'team-a', 'team-b']);

    fireEvent.click(screen.getByRole('option', { name: 'team-b' }));

    expect(vi.mocked(useWebSocket)).toHaveBeenLastCalledWith('/ws/local-queues?namespace=team-b');
  });

  it('names the namespace in the empty message of a filtered view', () => {
    mockWebSocket({
      '/ws/namespaces': namespaces,
      '/ws/local-queues': { data: [lq('team-a', 'q1')], error: null },
      '/ws/local-queues?namespace=team-b': { data: [], error: null },
    });
    renderWithRouter(<LocalQueues />);

    fireEvent.mouseDown(screen.getByRole('combobox'));
    fireEvent.click(screen.getByRole('option', { name: 'team-b' }));

    expect(screen.getByText('No Local Queues found in namespace "team-b".')).toBeInTheDocument();
  });
});
