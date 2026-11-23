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
import ClusterQueueDetail from './ClusterQueueDetail';
import { mockWebSocket, renderAtRoute } from './testing/helpers';

vi.mock('./useWebSocket', () => ({ default: vi.fn() }));

afterEach(cleanup);

const GiB = 1024 ** 3;

const clusterQueue = (overrides = {}) => ({
  metadata: { name: 'cq-1', uid: 'uid-1', creationTimestamp: '2026-01-02T03:04:05Z' },
  spec: {
    cohortName: 'team',
    flavorFungibility: { whenCanBorrow: 'TryNextFlavor', whenCanPreempt: 'MayStopSearch' },
    preemption: {
      reclaimWithinCohort: 'Any',
      borrowWithinCohort: { policy: 'LowerPriority' },
      withinClusterQueue: 'LowerPriority',
    },
    resourceGroups: [
      {
        flavors: [
          {
            name: 'on-demand',
            resources: [
              { name: 'cpu', nominalQuota: 4, borrowingLimit: 2, lendingLimit: 1 },
              { name: 'memory', nominalQuota: 2 * GiB },
            ],
          },
        ],
      },
    ],
  },
  status: {
    admittedWorkloads: 5,
    reservingWorkloads: 6,
    pendingWorkloads: 7,
    flavorsUsage: [
      {
        name: 'on-demand',
        resources: [
          { name: 'cpu', total: 5, borrowed: 1 },
          { name: 'memory', total: GiB },
        ],
      },
    ],
    flavorsReservation: [{ name: 'on-demand', resources: [{ name: 'cpu', total: 3 }] }],
  },
  queues: [
    {
      namespace: 'ns-1',
      name: 'lq-b',
      reservation: [{ name: 'on-demand', resources: [{ name: 'cpu', total: 2 }, { name: 'memory', total: GiB }] }],
      usage: [{ name: 'on-demand', resources: [{ name: 'cpu', total: 1 }] }],
    },
    { namespace: 'ns-1', name: 'lq-a', reservation: [{ name: 'spot', resources: [{ name: 'cpu', total: 1 }] }] },
  ],
  ...overrides,
});

const render = (response) => {
  mockWebSocket({ '/ws/cluster-queue/cq-1': response });
  return renderAtRoute(<ClusterQueueDetail />, '/cluster-queue/:clusterQueueName', '/cluster-queue/cq-1');
};

// The page has two tables, the resource quotas and the local queues. Find each
// one by a column header that only it has.
const tableWithHeader = (header) =>
  screen
    .getAllByRole('table')
    .find((table) => within(table).queryByRole('columnheader', { name: header }));
const quotaTable = () => tableWithHeader('Nominal Quota');
const localQueueTable = () => tableWithHeader('Queue Name');
const headers = (table) => within(table).getAllByRole('columnheader').map((header) => header.textContent);
const quotaRow = (resource) => within(quotaTable()).getByText(resource, { selector: 'td' }).closest('tr');
const cells = (row) => within(row).getAllByRole('cell').map((cell) => cell.textContent);

describe('ClusterQueueDetail', () => {
  it('shows a loading state until the first message arrives', () => {
    render({ data: null, error: null });

    expect(screen.getByText('Loading...')).toBeInTheDocument();
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('shows the error instead of the page', () => {
    render({ data: null, error: 'WebSocket connection failed.' });

    expect(screen.getByText('WebSocket connection failed.')).toBeInTheDocument();
  });

  it('renders the metadata, cohort link and workload counts', () => {
    render({ data: clusterQueue(), error: null });

    expect(screen.getByRole('heading', { name: 'Cluster Queue Detail: cq-1' })).toBeInTheDocument();
    expect(screen.getByText('uid-1')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'team' })).toHaveAttribute('href', '/cohort/team');
    expect(screen.getByText(/Admitted Workloads:/).parentElement).toHaveTextContent('Admitted Workloads: 5');
    expect(screen.getByText(/Reserving Workloads:/).parentElement).toHaveTextContent('Reserving Workloads: 6');
    expect(screen.getByText(/Pending Workloads:/).parentElement).toHaveTextContent('Pending Workloads: 7');
  });

  it('shows placeholders for a standalone queue without status', () => {
    render({ data: clusterQueue({ spec: {}, status: undefined, queues: undefined }), error: null });

    expect(screen.getByText(/^Cohort:/).parentElement).toHaveTextContent('Cohort: -');
    expect(screen.queryAllByRole('link').filter((link) => link.getAttribute('href').startsWith('/cohort/'))).toHaveLength(0);
    expect(screen.getByText(/Admitted Workloads:/).parentElement).toHaveTextContent('Admitted Workloads: N/A');
    expect(screen.getByText('No resource groups defined for this cluster queue.')).toBeInTheDocument();
    expect(screen.getByText('No local queues using this cluster queue')).toBeInTheDocument();
  });

  it('renders the configured fungibility and preemption settings', () => {
    render({ data: clusterQueue(), error: null });

    expect(screen.getByText(/When Can Borrow:/).parentElement).toHaveTextContent('TryNextFlavor');
    expect(screen.getByText(/When Can Preempt:/).parentElement).toHaveTextContent('MayStopSearch');
    expect(screen.getByText(/Reclaim Within Cohort:/).parentElement).toHaveTextContent('Any');
    expect(screen.getByText(/Borrow Within Cohort:/).parentElement).toHaveTextContent('LowerPriority');
    expect(screen.getByText(/Within ClusterQueue:/).parentElement).toHaveTextContent('LowerPriority');
  });

  it('falls back to the documented defaults when nothing is configured', () => {
    render({ data: clusterQueue({ spec: {} }), error: null });

    expect(screen.getByText(/When Can Borrow:/).parentElement).toHaveTextContent('Default (MayStopSearch)');
    expect(screen.getByText(/When Can Preempt:/).parentElement).toHaveTextContent('Default (TryNextFlavor)');
    expect(screen.getAllByText(/Not configured/)).toHaveLength(3);
  });

  it('renders quota, usage, borrowing and lending per resource', () => {
    render({ data: clusterQueue(), error: null });

    // Flavor (rowspan, first row only), resource, quota, usage, borrowed,
    // borrowing limit, lending limit and the utilization bar.
    expect(cells(quotaRow('cpu'))).toEqual(['on-demand', 'cpu', '4', '5', '1', '2', '1', '125%']);
    // The second resource shares the flavor cell and has no limits.
    expect(cells(quotaRow('memory'))).toEqual(['memory', '2Gi', '1Gi', '0', '', '', '50%']);
    expect(within(quotaRow('cpu')).getByRole('link', { name: 'on-demand' })).toHaveAttribute(
      'href',
      '/resource-flavor/on-demand'
    );
  });

  it('shows the reservation column only on request', () => {
    render({ data: clusterQueue(), error: null });
    // The local queue table has a "Reservation" column of its own, so scope
    // the lookup to the quota table.
    const quotas = () => within(quotaTable());
    expect(quotas().queryByRole('columnheader', { name: 'Reservation' })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('switch', { name: 'Show Reservation' }));

    const columns = headers(quotaTable());
    const row = cells(quotaRow('cpu'));
    expect(row[columns.indexOf('Reservation')]).toBe('3');
    expect(row[columns.indexOf('Usage')]).toBe('5');

    fireEvent.click(screen.getByRole('switch', { name: 'Show Reservation' }));
    expect(quotas().queryByRole('columnheader', { name: 'Reservation' })).not.toBeInTheDocument();
  });

  it('formats small cpu values in millicores', () => {
    const data = clusterQueue();
    data.spec.resourceGroups[0].flavors[0].resources = [{ name: 'cpu', nominalQuota: 0.5 }];
    data.status.flavorsUsage = [];
    render({ data, error: null });

    expect(cells(quotaRow('cpu')).slice(2, 4)).toEqual(['500m', '0']);
  });

  it('lists the local queues sorted by name with their namespace, reservation and usage', () => {
    render({ data: clusterQueue(), error: null });

    const links = screen.getAllByRole('link', { name: /^ns-1\/lq-/ });
    expect(links.map((link) => link.textContent)).toEqual(['ns-1/lq-a', 'ns-1/lq-b']);
    expect(links[1]).toHaveAttribute('href', '/local-queue/ns-1/lq-b');

    const cpuRow = screen.getByRole('link', { name: 'ns-1/lq-b' }).closest('tr');
    expect(cells(cpuRow)).toEqual(['ns-1/lq-b', 'on-demand', 'cpu', '2', '1']);
    // No usage entry for memory: N/A. The queue and flavor cells are spanned.
    const memoryRow = within(localQueueTable()).getByText('memory').closest('tr');
    expect(cells(memoryRow)).toEqual(['memory', '1Gi', 'N/A']);
    expect(screen.getByRole('link', { name: 'spot' })).toHaveAttribute('href', '/resource-flavor/spot');
  });

  it('tells local queues with the same name in different namespaces apart', () => {
    const reservation = [{ name: 'on-demand', resources: [{ name: 'cpu', total: 1 }] }];
    render({
      data: clusterQueue({
        queues: [
          { namespace: 'team-a', name: 'user-queue', reservation },
          { namespace: 'team-b', name: 'user-queue', reservation },
        ],
      }),
      error: null,
    });

    expect(screen.getByRole('link', { name: 'team-a/user-queue' })).toHaveAttribute('href', '/local-queue/team-a/user-queue');
    expect(screen.getByRole('link', { name: 'team-b/user-queue' })).toHaveAttribute('href', '/local-queue/team-b/user-queue');
  });

  it('keeps a local queue visible when it has no reservations', () => {
    const reservation = [{ name: 'on-demand', resources: [{ name: 'cpu', total: 1 }] }];
    render({
      data: clusterQueue({
        queues: [
          { namespace: 'team-a', name: 'user-queue', reservation: [], usage: [] },
          { namespace: 'team-b', name: 'user-queue', reservation, usage: [] },
        ],
      }),
      error: null,
    });

    const row = screen.getByRole('row', { name: 'team-a/user-queue No reservation data' });
    expect(within(row).getByRole('link', { name: 'team-a/user-queue' })).toHaveAttribute('href', '/local-queue/team-a/user-queue');
    expect(screen.getByRole('link', { name: 'team-b/user-queue' })).toHaveAttribute('href', '/local-queue/team-b/user-queue');
  });

  it('labels the first resource row even if the first flavor has no resources', () => {
    render({
      data: clusterQueue({
        queues: [
          {
            namespace: 'team-a',
            name: 'user-queue',
            reservation: [
              { name: 'empty', resources: [] },
              { name: 'on-demand', resources: [{ name: 'cpu', total: 1 }, { name: 'memory', total: 1024 }] },
            ],
            usage: [],
          },
        ],
      }),
      error: null,
    });

    const row = screen.getByRole('row', { name: /team-a\/user-queue on-demand cpu/ });
    expect(within(row).getByRole('cell', { name: 'team-a/user-queue' })).toHaveAttribute('rowspan', '2');
  });
});
