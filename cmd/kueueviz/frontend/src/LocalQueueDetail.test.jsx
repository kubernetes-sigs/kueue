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
import LocalQueueDetail from './LocalQueueDetail';
import { mockWebSocket, renderAtRoute } from './testing/helpers';

vi.mock('./useWebSocket', () => ({ default: vi.fn() }));

afterEach(cleanup);

const QUEUE_URL = '/ws/local-queue/ns-1/lq-1';
const WORKLOADS_URL = `${QUEUE_URL}/workloads`;

const queue = () => ({
  metadata: { name: 'lq-1', namespace: 'ns-1', uid: 'uid-1', creationTimestamp: '2026-01-02T03:04:05Z' },
  spec: { clusterQueue: 'cq-1' },
  status: {
    admittedWorkloads: 1,
    pendingWorkloads: 2,
    reservingWorkloads: 3,
    conditions: [
      { type: 'Active', status: 'True', reason: 'Ready', lastTransitionTime: '2026-01-02T03:04:05Z' },
    ],
    flavorsUsage: [{ name: 'on-demand', resources: [{ name: 'cpu', total: '2' }] }],
    flavorsReservation: [{ name: 'spot', resources: [{ name: 'memory', total: '4Gi' }] }],
  },
});

const workload = (name, conditions = []) => ({ metadata: { name }, status: { conditions } });

const render = ({ queueResponse, workloadsResponse }) => {
  mockWebSocket({
    [QUEUE_URL]: queueResponse ?? { data: queue(), error: null },
    [WORKLOADS_URL]: workloadsResponse ?? { data: [], error: null },
  });
  return renderAtRoute(<LocalQueueDetail />, '/local-queue/:namespace/:queueName', '/local-queue/ns-1/lq-1');
};

describe('LocalQueueDetail', () => {
  it('shows a spinner until the queue arrives', () => {
    render({ queueResponse: { data: null, error: null } });

    expect(screen.getByRole('progressbar')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: /Local Queue Detail/ })).not.toBeInTheDocument();
  });

  it.each([
    ['queue', { queueResponse: { data: null, error: 'queue failed' } }, 'queue failed'],
    ['workloads', { workloadsResponse: { data: null, error: 'workloads failed' } }, 'workloads failed'],
  ])('shows the %s error instead of the page', (_, props, message) => {
    render(props);

    expect(screen.getByText(message)).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: /Local Queue Detail/ })).not.toBeInTheDocument();
  });

  it('renders the metadata, cluster queue link and workload counts', () => {
    render({});

    expect(screen.getByRole('heading', { name: 'Local Queue Detail: lq-1' })).toBeInTheDocument();
    expect(screen.getByText('uid-1')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'cq-1' })).toHaveAttribute('href', '/cluster-queue/cq-1');
    expect(screen.getByText(/Admitted Workloads:/).parentElement).toHaveTextContent('Admitted Workloads: 1');
    expect(screen.getByText(/Pending Workloads:/).parentElement).toHaveTextContent('Pending Workloads: 2');
    expect(screen.getByText(/Reserving Workloads:/).parentElement).toHaveTextContent('Reserving Workloads: 3');
  });

  it('lists the conditions', () => {
    render({});

    const row = screen.getByText('Active').closest('tr');
    expect(within(row).getByText('True')).toBeInTheDocument();
    expect(within(row).getByText('Ready')).toBeInTheDocument();
  });

  it('renders the flavor usage and reservation tables with links to the flavors', () => {
    render({});

    expect(screen.getByRole('heading', { name: 'Flavor Usage' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Flavors Reservation' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'on-demand' })).toHaveAttribute('href', '/resource-flavor/on-demand');
    expect(screen.getByRole('link', { name: 'spot' })).toHaveAttribute('href', '/resource-flavor/spot');
    expect(screen.getByText('4Gi')).toBeInTheDocument();
  });

  it('lists the workloads with their status, linking to their detail page', () => {
    render({
      workloadsResponse: {
        data: [
          workload('wl-pending'),
          workload('wl-reserved', [{ type: 'QuotaReserved', status: 'True' }]),
          workload('wl-admitted', [{ type: 'Admitted', status: 'True' }]),
          workload('wl-finished', [{ type: 'Admitted', status: 'True' }, { type: 'Finished', status: 'True' }]),
        ],
        error: null,
      },
    });

    const expected = {
      'wl-pending': 'Pending',
      'wl-reserved': 'QuotaReserved',
      'wl-admitted': 'Admitted',
      'wl-finished': 'Finished',
    };
    Object.entries(expected).forEach(([name, status]) => {
      const link = screen.getByRole('link', { name });
      expect(link).toHaveAttribute('href', `/workload/ns-1/${name}`);
      expect(within(link.closest('tr')).getByText(status)).toBeInTheDocument();
    });
  });

  it('renders without conditions or usage', () => {
    const data = queue();
    data.status = { admittedWorkloads: 0 };
    render({ queueResponse: { data, error: null } });

    expect(screen.getByRole('heading', { name: 'Local Queue Detail: lq-1' })).toBeInTheDocument();
  });
});
