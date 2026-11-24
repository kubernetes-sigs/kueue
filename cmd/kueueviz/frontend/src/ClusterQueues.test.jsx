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
import { cleanup, screen, within } from '@testing-library/react';
import ClusterQueues from './ClusterQueues';
import { mockWebSocket, renderWithRouter } from './testing/helpers';

vi.mock('./useWebSocket', () => ({ default: vi.fn() }));

vi.mock('./ViewYamlButton', () => ({
  default: ({ resourceType, resourceName }) => <span>{`yaml:${resourceType}/${resourceName}`}</span>,
}));

afterEach(cleanup);

const render = (response) => {
  mockWebSocket({ '/ws/cluster-queues': response });
  return renderWithRouter(<ClusterQueues />);
};

const queue = (overrides = {}) => ({
  name: 'cq-a',
  cohort: 'team',
  flavors: ['on-demand', 'spot'],
  admittedWorkloads: 3,
  pendingWorkloads: 1,
  reservingWorkloads: 4,
  resourceGroups: [{ flavors: [{ name: 'on-demand', resources: [{ name: 'cpu', nominalQuota: 4 }] }] }],
  flavorsUsage: [{ name: 'on-demand', resources: [{ name: 'cpu', total: 2, borrowed: 0 }] }],
  ...overrides,
});

describe('ClusterQueues', () => {
  it('shows a spinner until the first message arrives', () => {
    render({ data: null, error: null });

    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('shows the error instead of the page', () => {
    render({ data: null, error: 'WebSocket connection failed.' });

    expect(screen.getByText('WebSocket connection failed.')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Cluster Queues' })).not.toBeInTheDocument();
  });

  it('shows an empty message when there are no cluster queues', () => {
    render({ data: [], error: null });

    expect(screen.getByText('No Cluster Queues found.')).toBeInTheDocument();
  });

  it('sorts the queues by name', () => {
    render({ data: [queue({ name: 'cq-b' }), queue({ name: 'cq-a' })], error: null });

    const links = screen.getAllByRole('link', { name: /^cq-/ });
    expect(links.map((link) => link.textContent)).toEqual(['cq-a', 'cq-b']);
    expect(links[0]).toHaveAttribute('href', '/cluster-queue/cq-a');
  });

  it('renders the cohort, flavors and workload counts of a queue', () => {
    render({ data: [queue()], error: null });

    const row = screen.getByRole('link', { name: 'cq-a' }).closest('tr');
    expect(within(row).getByRole('link', { name: 'team' })).toHaveAttribute('href', '/cohort/team');
    expect(within(row).getByRole('link', { name: 'on-demand' })).toHaveAttribute('href', '/resource-flavor/on-demand');
    expect(within(row).getByRole('link', { name: 'spot' })).toHaveAttribute('href', '/resource-flavor/spot');
    expect(row).toHaveTextContent('on-demand, spot');
    ['3', '1', '4'].forEach((count) => expect(within(row).getByText(count)).toBeInTheDocument());
    expect(within(row).getByText('yaml:clusterqueue/cq-a')).toBeInTheDocument();
  });

  it('shows placeholders for a queue without cohort, flavors and counts', () => {
    render({ data: [{ name: 'bare' }], error: null });

    const row = screen.getByRole('link', { name: 'bare' }).closest('tr');
    expect(within(row).queryByRole('link', { name: /team/ })).not.toBeInTheDocument();
    expect(within(row).getAllByText('N/A')).toHaveLength(4);
  });

  it('adds a column per resource, cpu and memory first, with a usage bar per queue', () => {
    render({
      data: [
        queue({
          resourceGroups: [
            {
              flavors: [
                {
                  name: 'on-demand',
                  resources: [
                    { name: 'nvidia.com/gpu', nominalQuota: 1 },
                    { name: 'memory', nominalQuota: 1024 ** 3 },
                    { name: 'cpu', nominalQuota: 4 },
                  ],
                },
              ],
            },
          ],
          flavorsUsage: [{ name: 'on-demand', resources: [{ name: 'cpu', total: 2, borrowed: 0 }] }],
        }),
      ],
      error: null,
    });

    const headers = screen.getAllByRole('columnheader').map((header) => header.textContent);
    expect(headers.slice(-4)).toEqual(['cpu', 'memory', 'nvidia.com/gpu', 'Actions']);
    expect(screen.getAllByTestId('usage-bar')).toHaveLength(3);
  });

  it('shows a dash for a resource that a queue has neither quota nor usage for', () => {
    render({
      data: [
        queue({ name: 'cq-a' }),
        queue({ name: 'cq-b', resourceGroups: [], flavorsUsage: [] }),
      ],
      error: null,
    });

    const row = screen.getByRole('link', { name: 'cq-b' }).closest('tr');
    expect(within(row).queryByTestId('usage-bar')).not.toBeInTheDocument();
    expect(within(row).getByText('-')).toBeInTheDocument();
  });
});
