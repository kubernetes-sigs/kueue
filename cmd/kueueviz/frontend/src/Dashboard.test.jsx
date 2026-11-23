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
import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react';
import Dashboard, { PREEMPTOR_UID_PATTERN, splitPreemptionMessage } from './Dashboard';
import useWebSocket from './useWebSocket';
import { toast } from 'react-toastify';
import { mockWebSocket, renderWithRouter } from './testing/helpers';

vi.mock('./useWebSocket', () => ({
  default: vi.fn(),
}));

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

const makeWorkload = (namespace, podName) => ({
  metadata: { namespace, name: 'wl', uid: `${namespace}-uid` },
  spec: { queueName: 'lq', podSets: [{ count: 1 }] },
  status: { conditions: [] },
  pods: [{ name: podName, status: { phase: 'Running' } }],
});

const DASHBOARD_URL = '/ws/workloads/dashboard?page=0&pageSize=25';
const namespacesResponse = { data: { namespaces: ['ns-b', 'ns-a'] }, error: null };

const dashWorkload = (namespace, name, extra = {}) => ({
  metadata: { namespace, name, uid: `${namespace}-${name}-uid` },
  spec: { queueName: 'lq', priority: 5, podSets: [{ count: 2 }, { count: 1 }] },
  status: { conditions: [] },
  pods: [],
  ...extra,
});

const cq = (name, cohort, quota, usage) => ({
  name,
  cohort,
  resourceGroups: [{ flavors: [{ name: 'f', resources: [{ name: 'cpu', nominalQuota: quota }] }] }],
  flavorsUsage: [{ name: 'f', resources: [{ name: 'cpu', total: usage }] }],
});

const renderDashboard = (dashboardData, { namespaces = namespacesResponse, extra = {} } = {}) => {
  mockWebSocket({
    '/ws/namespaces': namespaces,
    [DASHBOARD_URL]: dashboardData,
    ...extra,
  });
  return renderWithRouter(<Dashboard />);
};

const payload = ({ workloads = [], queues = [], clusterQueues = [], byUid = {}, page = 0, pageSize = 25, totalCount = workloads.length, finishedCount = workloads.filter(w => w.status?.conditions?.some(c => c.type === 'Finished' && c.status === 'True')).length } = {}) => ({
  data: { queues, clusterQueues, workloads: { items: workloads, workloads_by_uid: byUid, pagination: { page, pageSize, totalCount }, finishedCount } },
  error: null,
});

describe('PREEMPTOR_UID_PATTERN', () => {
  it('extracts the preemptor workload UID from a Kueue eviction message', () => {
    const message =
      'Evicted: Preempted to accommodate a workload (UID: 1a2b-3c4d, JobUID: 5e6f-7a8b) due to prioritization in the ClusterQueue; preemptor path: /cq; preemptee path: /cq';

    const parts = message.split(PREEMPTOR_UID_PATTERN);

    expect(parts).toEqual([
      'Evicted: Preempted to accommodate a workload ',
      '1a2b-3c4d',
      ', JobUID: 5e6f-7a8b) due to prioritization in the ClusterQueue; preemptor path: /cq; preemptee path: /cq',
    ]);
  });

  it('does not match the JobUID', () => {
    expect('(JobUID: 5e6f-7a8b)'.split(PREEMPTOR_UID_PATTERN)).toEqual(['(JobUID: 5e6f-7a8b)']);
  });
});

describe('splitPreemptionMessage', () => {
  const message = 'Evicted: Preempted to accommodate a workload (UID: 1a2b-3c4d, JobUID: 5e6f-7a8b) due to prioritization';

  it('links the preemptor UID when the workload is known', () => {
    expect(splitPreemptionMessage(message, { '1a2b-3c4d': 'ns-1/wl-1' })).toEqual([
      { text: 'Evicted: Preempted to accommodate a workload ' },
      { text: '(UID: 1a2b-3c4d', to: '/workload/ns-1/wl-1' },
      { text: ', JobUID: 5e6f-7a8b) due to prioritization' },
    ]);
  });

  it('keeps the full message text when the preemptor is not known', () => {
    const segments = splitPreemptionMessage(message, {});

    expect(segments.map((segment) => segment.text).join('')).toBe(message);
    expect(segments.some((segment) => segment.to)).toBe(false);
  });

  it('returns the message unchanged when it has no preemptor UID', () => {
    expect(splitPreemptionMessage('Evicted: InactiveWorkload', {})).toEqual([
      { text: 'Evicted: InactiveWorkload' },
    ]);
  });
});

describe('Dashboard workload rows', () => {
  it('expands only the clicked workload when two namespaces use the same name', () => {
    renderDashboard(payload({ workloads: [makeWorkload('ns-a', 'pod-in-a'), makeWorkload('ns-b', 'pod-in-b')] }));

    const rowA = screen.getByText('ns-a').closest('tr');
    fireEvent.click(within(rowA).getByRole('button'));

    expect(screen.getByText('pod-in-a')).toBeInTheDocument();
    expect(screen.queryByText('pod-in-b')).not.toBeInTheDocument();
  });
});

describe('Dashboard page', () => {
  it('shows a loading message until the first message arrives', () => {
    renderDashboard({ data: null, error: null });

    expect(screen.getByText('Loading...')).toBeInTheDocument();
  });

  it.each([
    ['dashboard', { data: null, error: 'dashboard failed' }, undefined, 'dashboard failed'],
    ['namespaces', { data: null, error: null }, { namespaces: { data: null, error: 'ns failed' } }, 'ns failed'],
  ])('shows the %s error instead of the page', (_, response, options, message) => {
    renderDashboard(response, options);

    expect(screen.getByText(message)).toBeInTheDocument();
    expect(screen.queryByText('Total Workloads')).not.toBeInTheDocument();
  });

  it('counts local queues, workloads and finished workloads', () => {
    renderDashboard(
      payload({
        queues: [{}, {}],
        workloads: [
          dashWorkload('ns-a', 'a'),
          dashWorkload('ns-a', 'b', { status: { conditions: [{ type: 'Finished', status: 'True' }] } }),
          dashWorkload('ns-a', 'c', { status: { conditions: [{ type: 'Finished', status: 'False' }] } }),
        ],
      })
    );

    const count = (title) => screen.getByText(title).nextElementSibling.textContent;
    expect(count('Total Local Queues')).toBe('2');
    expect(count('Total Workloads')).toBe('3');
    expect(count('Completed Workloads')).toBe('1');
  });

  it('handles a payload without queues or workloads', () => {
    renderDashboard({ data: { workloads: { pagination: { page: 0, pageSize: 25, totalCount: 0 }, finishedCount: 0 } }, error: null });

    expect(screen.getByText('Total Workloads').nextElementSibling).toHaveTextContent('0');
  });

  describe('resource utilization by cohort', () => {
    it('groups the cluster queues per cohort, standalone queues last', () => {
      renderDashboard(
        payload({
          clusterQueues: [cq('cq-1', '', 4, 1), cq('cq-2', 'team-b', 4, 2), cq('cq-3', 'team-a', 2, 2), cq('cq-4', 'team-b', 4, 4)],
        })
      );

      const table = screen.getByRole('heading', { name: 'Resource Utilization by Cohort' }).nextElementSibling;
      const rows = within(table).getAllByRole('row').slice(1);
      expect(rows.map((row) => within(row).getAllByRole('cell').slice(0, 2).map((cell) => cell.textContent))).toEqual([
        ['team-a', '1'],
        ['team-b', '2'],
        ['Standalone', '1'],
      ]);
      expect(within(rows[0]).getByRole('link', { name: 'team-a' })).toHaveAttribute('href', '/cohort/team-a');
    });

    it('aggregates usage and quota over the queues of a cohort', () => {
      // 10 used of 12 nominal is 83%, whereas the average of the two queues'
      // percentages (50% and 100%) would be 75%.
      renderDashboard(payload({ clusterQueues: [cq('cq-2', 'team', 4, 2), cq('cq-4', 'team', 8, 8)] }));

      expect(screen.getByText('83%')).toBeInTheDocument();
      expect(screen.queryByText('75%')).not.toBeInTheDocument();
    });

    it('is not shown without cluster queues or resources', () => {
      renderDashboard(payload({ clusterQueues: [{ name: 'bare' }] }));

      expect(screen.queryByRole('heading', { name: 'Resource Utilization by Cohort' })).not.toBeInTheDocument();
    });
  });

  describe('workload rows', () => {
    it('preserves the server workload order', () => {
      renderDashboard(payload({ workloads: [dashWorkload('ns-b', 'a'), dashWorkload('ns-a', 'z'), dashWorkload('ns-a', 'b')] }));

      const rows = screen.getAllByRole('link', { name: /^(a|b|z)$/ });
      expect(rows.map((link) => link.getAttribute('href'))).toEqual(['/workload/ns-b/a', '/workload/ns-a/z', '/workload/ns-a/b']);
    });

    it('renders the details of a workload', () => {
      renderDashboard(
        payload({
          workloads: [
            dashWorkload('ns-a', 'wl', {
              status: {
                conditions: [{ type: 'Admitted', status: 'True', reason: 'Admitted' }],
                admission: { clusterQueue: 'cq-1' },
              },
            }),
          ],
        })
      );

      const row = screen.getByRole('link', { name: 'wl' }).closest('tr');
      expect(within(row).getByRole('link', { name: 'lq' })).toHaveAttribute('href', '/local-queue/ns-a/lq');
      expect(within(row).getByRole('link', { name: 'cq-1' })).toHaveAttribute('href', '/cluster-queue/cq-1');
      // 2 + 1 pods.
      expect(within(row).getByText('3')).toBeInTheDocument();
      expect(within(row).getByText('5')).toBeInTheDocument();
      expect(row.textContent).not.toContain('Not admitted');
    });

    it('explains why a workload is not admitted', () => {
      renderDashboard(
        payload({
          workloads: [
            dashWorkload('ns-a', 'wl', {
              status: { conditions: [{ type: 'Admitted', status: 'False', reason: 'NoReservation' }] },
            }),
          ],
        })
      );

      expect(screen.getByText('Not admitted: NoReservation')).toBeInTheDocument();
    });

    it('shows N/A for a workload without admission, priority and class', () => {
      renderDashboard(
        payload({
          workloads: [{ metadata: { namespace: 'ns-a', name: 'wl' }, spec: { queueName: 'lq' }, status: {} }],
        })
      );

      const row = screen.getByRole('link', { name: 'wl' }).closest('tr');
      expect(within(row).getAllByText('N/A')).toHaveLength(3);
      expect(row.textContent).toContain('Not admitted: N/A');
    });

    it('marks finished and unfinished workloads with different icons', () => {
      renderDashboard(
        payload({
          workloads: [
            dashWorkload('ns-a', 'done', { status: { conditions: [{ type: 'Finished', status: 'True' }] } }),
            dashWorkload('ns-a', 'todo'),
          ],
        })
      );

      const iconOf = (name) => screen.getByRole('link', { name }).closest('tr').querySelector('.status-column svg').getAttribute('data-testid');
      expect(iconOf('done')).toBe('CheckCircleIcon');
      expect(iconOf('todo')).toBe('AccessTimeIcon');
    });
  });

  describe('expanding rows', () => {
    const workloads = () => [
      dashWorkload('ns-a', 'one', {
        pods: [
          { name: 'pod-running', status: { phase: 'Running' } },
          {
            name: 'pod-pending',
            status: {
              phase: 'Pending',
              conditions: [{ type: 'PodScheduled', status: 'False', reason: 'Unschedulable', message: '0/3 nodes' }],
            },
          },
          { name: 'pod-pending-unknown', status: { phase: 'Pending' } },
        ],
      }),
      dashWorkload('ns-a', 'two', { pods: [{ name: 'pod-two', status: { phase: 'Running' } }] }),
    ];

    it('hides the pods by default', () => {
      renderDashboard(payload({ workloads: workloads() }));

      expect(screen.queryByText('pod-running')).not.toBeInTheDocument();
    });

    it('shows the pods of an expanded workload with their pending reason', () => {
      renderDashboard(payload({ workloads: workloads() }));

      fireEvent.click(within(screen.getByRole('link', { name: 'one' }).closest('tr')).getByRole('button'));

      expect(screen.getByText('pod-running')).toBeInTheDocument();
      const pending = screen.getByText('pod-pending').closest('tr');
      expect(within(pending).getByText('Unschedulable')).toBeInTheDocument();
      // A pending pod without a scheduling condition has no reason.
      expect(within(screen.getByText('pod-pending-unknown').closest('tr')).getByText('N/A')).toBeInTheDocument();
      expect(screen.queryByText('pod-two')).not.toBeInTheDocument();
    });

    it('collapses a workload again', async () => {
      renderDashboard(payload({ workloads: workloads() }));
      const toggle = within(screen.getByRole('link', { name: 'one' }).closest('tr')).getByRole('button');

      fireEvent.click(toggle);
      expect(screen.getByText('pod-running')).toBeInTheDocument();

      fireEvent.click(toggle);

      await waitFor(() => expect(screen.queryByText('pod-running')).not.toBeInTheDocument());
    });

    it('expands and collapses all workloads at once', async () => {
      renderDashboard(payload({ workloads: workloads() }));
      const toggleAll = within(screen.getAllByRole('columnheader')[0]).getByRole('button');

      fireEvent.click(toggleAll);
      expect(screen.getByText('pod-running')).toBeInTheDocument();
      expect(screen.getByText('pod-two')).toBeInTheDocument();

      fireEvent.click(toggleAll);
      await waitFor(() => expect(screen.queryByText('pod-two')).not.toBeInTheDocument());
      expect(screen.queryByText('pod-running')).not.toBeInTheDocument();
    });
  });

  describe('namespace filter', () => {
    it('requests the selected namespace and lists the namespaces sorted', () => {
      renderDashboard(payload());

      fireEvent.mouseDown(screen.getByRole('combobox', { name: 'Filter by namespace' }));
      expect(screen.getAllByRole('option').map((option) => option.textContent)).toEqual(['All', 'ns-a', 'ns-b']);
      fireEvent.click(screen.getByRole('option', { name: 'ns-b' }));

      expect(vi.mocked(useWebSocket)).toHaveBeenLastCalledWith(`${DASHBOARD_URL}&namespace=ns-b`);
    });
  });

  describe('preemption', () => {
    const victim = (name, message) =>
      dashWorkload('ns-a', name, {
        metadata: { namespace: 'ns-a', name, uid: `${name}-uid` },
        preemption: { preempted: true, reason: 'InClusterQueue' },
        status: { conditions: [{ type: 'Evicted', reason: 'Preempted', status: 'True', message }] },
      });
    const preempted = (message, byUid) => payload({ byUid, workloads: [victim('victim', message)] });

    it('shows a column entry only for preempted workloads', () => {
      renderDashboard(payload({ workloads: [dashWorkload('ns-a', 'plain')] }));

      const row = screen.getByRole('link', { name: 'plain' }).closest('tr');
      expect(row.querySelector('.preempted-column')).toBeEmptyDOMElement();
      expect(screen.queryByText('Evicted')).not.toBeInTheDocument();
    });

    it('links the preemptor in the tooltip to its workload page', async () => {
      renderDashboard(
        preempted('Preempted to accommodate a workload (UID: abc-123, JobUID: j1) due to prioritization', {
          'abc-123': 'ns-a/preemptor',
        })
      );

      fireEvent.mouseOver(screen.getByText('Evicted'));

      const link = await screen.findByRole('link', { name: '(UID: abc-123' });
      expect(link).toHaveAttribute('href', '/workload/ns-a/preemptor');
      expect(screen.getByRole('tooltip')).toHaveTextContent('Evicted: Preempted to accommodate a workload');
    });

    it('does not link a preemptor that is not a known workload', async () => {
      renderDashboard(preempted('Preempted (UID: def-456, JobUID: j1)', {}));

      fireEvent.mouseOver(screen.getByText('Evicted'));

      expect(await screen.findByRole('tooltip')).toHaveTextContent('(UID: def-456');
      expect(screen.queryByRole('link', { name: '(UID: def-456' })).not.toBeInTheDocument();
    });

    it('raises a toast for each preempted workload', async () => {
      renderDashboard(preempted('Preempted', {}));

      expect(await screen.findByText('Workload victim was preempted: InClusterQueue')).toBeInTheDocument();
    });

    it('raises a separate toast for every preempted workload', async () => {
      renderDashboard(payload({ workloads: [victim('victim-1', 'Preempted'), victim('victim-2', 'Preempted')] }));

      expect(await screen.findByText('Workload victim-1 was preempted: InClusterQueue')).toBeInTheDocument();
      expect(await screen.findByText('Workload victim-2 was preempted: InClusterQueue')).toBeInTheDocument();
    });
  });
});


describe('Dashboard pagination', () => {
  it('uses full-scope totals while rendering only the current page', () => {
    renderDashboard(payload({ workloads: [dashWorkload('ns-a', 'one')], totalCount: 125, finishedCount: 99 }));
    expect(screen.getByText('Total Workloads').nextElementSibling).toHaveTextContent('125');
    expect(screen.getByText('Completed Workloads').nextElementSibling).toHaveTextContent('99');
    expect(screen.getAllByRole('link', { name: 'one' })).toHaveLength(1);
    expect(screen.getByText('1–25 of 125')).toBeInTheDocument();
  });

  it('requests the next page, clears expanded rows, and can return to the first page', () => {
    renderDashboard(payload({ workloads: [makeWorkload('ns-a', 'pod-a')], totalCount: 51 }), {
      extra: { '/ws/workloads/dashboard?page=1&pageSize=25': payload({ page: 1, totalCount: 51, workloads: [makeWorkload('ns-b', 'pod-b')] }) },
    });
    fireEvent.click(within(screen.getByRole('link', { name: 'wl' }).closest('tr')).getByRole('button'));
    expect(screen.getByText('pod-a')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Go to next page' }));
    expect(useWebSocket).toHaveBeenLastCalledWith('/ws/workloads/dashboard?page=1&pageSize=25');
    expect(screen.getByText('26–50 of 51')).toBeInTheDocument();
    expect(screen.queryByText('pod-a')).not.toBeInTheDocument();
    expect(screen.queryByText('pod-b')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Go to previous page' }));
    expect(useWebSocket).toHaveBeenLastCalledWith(DASHBOARD_URL);
    expect(screen.queryByText('pod-a')).not.toBeInTheDocument();
  });

  it('shows loading while waiting for a different page', () => {
    renderDashboard(payload({ totalCount: 51 }));
    fireEvent.click(screen.getByRole('button', { name: 'Go to next page' }));
    expect(screen.getByText('Loading...')).toBeInTheDocument();
  });

  it('resets the page when the namespace changes', () => {
    renderDashboard(payload({ totalCount: 51 }), {
      extra: { '/ws/workloads/dashboard?page=1&pageSize=25': payload({ page: 1, totalCount: 51 }) },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Go to next page' }));
    fireEvent.mouseDown(screen.getByRole('combobox', { name: 'Filter by namespace' }));
    fireEvent.click(screen.getByRole('option', { name: 'ns-a' }));
    expect(useWebSocket).toHaveBeenLastCalledWith(`${DASHBOARD_URL}&namespace=ns-a`);
  });

  it('offers bounded page sizes and resets the page on a size change', () => {
    renderDashboard(payload({ totalCount: 125 }), {
      extra: { '/ws/workloads/dashboard?page=1&pageSize=25': payload({ page: 1, totalCount: 125 }) },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Go to next page' }));
    fireEvent.mouseDown(screen.getByRole('combobox', { name: 'Rows per page:' }));
    expect(screen.getAllByRole('option').map(option => option.textContent)).toEqual(['25', '50', '100']);
    fireEvent.click(screen.getByRole('option', { name: '50' }));
    expect(useWebSocket).toHaveBeenLastCalledWith('/ws/workloads/dashboard?page=0&pageSize=50');
  });

  it('resubscribes to the effective page when live deletions remove the last page', () => {
    renderDashboard(payload({ totalCount: 51 }), {
      extra: { '/ws/workloads/dashboard?page=1&pageSize=25': payload({ page: 0, totalCount: 10 }) },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Go to next page' }));
    expect(useWebSocket).toHaveBeenLastCalledWith(DASHBOARD_URL);
  });

  it('disables navigation for an empty result', () => {
    renderDashboard(payload());
    expect(screen.getByText('0–0 of 0')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Go to next page' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Go to previous page' })).toBeDisabled();
  });

  it('explains that an older backend must be upgraded', () => {
    const notification = vi.spyOn(toast, 'error');
    renderDashboard({ data: { workloads: { items: [dashWorkload('ns-a', 'victim', { preemption: { preempted: true, reason: 'InClusterQueue' } })] } }, error: null });
    expect(screen.getByText(/Upgrade the backend before the frontend/)).toBeInTheDocument();
    expect(notification).not.toHaveBeenCalled();
  });
});
