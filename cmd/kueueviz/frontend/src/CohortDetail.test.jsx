// @vitest-environment jsdom
import { afterEach, describe, it, expect, vi } from 'vitest';
import '@testing-library/jest-dom/vitest';
import React from 'react';
import { cleanup, screen, within } from '@testing-library/react';
import CohortDetail from './CohortDetail';
import { mockWebSocket, renderAtRoute } from './testing/helpers';

afterEach(cleanup);

// Mock the useWebSocket hook
vi.mock('./useWebSocket', () => ({
  default: vi.fn(),
}));

const GiB = 1024 ** 3;

const cohortQueue = (name, overrides = {}) => ({
  name,
  spec: {
    flavorFungibility: { whenCanBorrow: 'TryNextFlavor', whenCanPreempt: 'MayStopSearch' },
    preemption: {
      borrowWithinCohort: { policy: 'LowerPriority' },
      reclaimWithinCohort: 'Any',
      withinClusterQueue: 'Never',
    },
    queueingStrategy: 'BestEffortFIFO',
    resourceGroups: [
      {
        flavors: [
          {
            name: 'on-demand',
            resources: [
              { name: 'cpu', nominalQuota: 4 },
              { name: 'memory', nominalQuota: 2 * GiB },
            ],
          },
        ],
      },
    ],
  },
  status: {
    flavorsUsage: [
      {
        name: 'on-demand',
        resources: [
          { name: 'cpu', total: 3, borrowed: 1 },
          { name: 'memory', total: GiB },
        ],
      },
    ],
  },
  ...overrides,
});

const renderCohort = (data) => {
  mockWebSocket({ '/ws/cohort/test-cohort': { data, error: null } });
  return renderAtRoute(<CohortDetail />, '/cohort/:cohortName', '/cohort/test-cohort');
};

describe('CohortDetail page', () => {
  it('shows a loading state until the cohort arrives', () => {
    renderCohort(null);

    expect(screen.getByText('Loading...')).toBeInTheDocument();
  });

  it('shows the error of the connection instead of the page', () => {
    mockWebSocket({ '/ws/cohort/test-cohort': { data: null, error: 'WebSocket connection failed' } });
    renderAtRoute(<CohortDetail />, '/cohort/:cohortName', '/cohort/test-cohort');

    expect(screen.getByText(/WebSocket connection failed/)).toBeInTheDocument();
  });

  // Regression test for #12571: the backend can answer with {"error": "..."}
  // instead of the Cohort object.
  it('shows the error of an API payload instead of the page', () => {
    renderCohort({ error: 'error fetching cohort' });

    expect(screen.getByText(/error fetching cohort/)).toBeInTheDocument();
  });

  it('shows the number of cluster queues and their names sorted', () => {
    renderCohort({ clusterQueues: [cohortQueue('cq-b'), cohortQueue('cq-a')] });

    expect(screen.getByText('Number of Cluster Queues:').parentElement).toHaveTextContent('Number of Cluster Queues: 2');
    const links = screen.getAllByRole('link', { name: /^cq-/ });
    // Once in the usage table and once in the settings table.
    expect(links.map((link) => link.textContent)).toEqual(['cq-a', 'cq-b', 'cq-a', 'cq-b']);
    expect(links[0]).toHaveAttribute('href', '/cluster-queue/cq-a');
  });

  it('aggregates the usage and quota of all queues per resource', () => {
    // cq-a uses 3 of 4 cpu and 1Gi of 2Gi memory (the defaults). cq-b has other
    // quotas and usage, so a total that ignores a queue or counts one twice is
    // detected.
    const cqB = cohortQueue('cq-b');
    cqB.spec.resourceGroups[0].flavors[0].resources = [
      { name: 'cpu', nominalQuota: 6 },
      { name: 'memory', nominalQuota: 3 * GiB },
    ];
    cqB.status.flavorsUsage[0].resources = [
      { name: 'cpu', total: 2 },
      { name: 'memory', total: 2 * GiB },
    ];
    renderCohort({ clusterQueues: [cohortQueue('cq-a'), cqB] });

    const heading = screen.getByRole('heading', { name: 'Cohort Resource Utilization' });
    expect(heading).toBeInTheDocument();
    // cpu: 3 + 2 used of 4 + 6 quota. memory: 1Gi + 2Gi used of 2Gi + 3Gi quota.
    expect(screen.getByText('5 / 10')).toBeInTheDocument();
    expect(screen.getByText('3Gi / 5Gi')).toBeInTheDocument();
  });

  it('breaks the usage down per queue and resource', () => {
    renderCohort({
      clusterQueues: [
        cohortQueue('cq-a'),
        cohortQueue('cq-b', {
          status: {
            flavorsUsage: [
              {
                name: 'on-demand',
                resources: [
                  { name: 'cpu', total: 1 },
                  { name: 'memory', total: 2 * GiB },
                ],
              },
            ],
          },
        }),
      ],
    });

    const table = screen.getByRole('heading', { name: 'Per-Queue Resource Usage' }).nextElementSibling;
    const headers = within(table).getAllByRole('columnheader').map((header) => header.textContent);
    expect(headers).toEqual(['Queue', 'cpu', 'memory']);
    // Usage as a share of the nominal quota, which differs per queue and resource.
    const row = (name) => within(within(table).getByRole('link', { name }).closest('tr'));
    expect(row('cq-a').getAllByRole('cell').map((cell) => cell.textContent)).toEqual(['cq-a', '75%', '50%']);
    expect(row('cq-b').getAllByRole('cell').map((cell) => cell.textContent)).toEqual(['cq-b', '25%', '100%']);
  });

  it('adds the weighted share column only when a queue reports fair sharing', () => {
    const { unmount } = renderCohort({ clusterQueues: [cohortQueue('cq-a')] });
    expect(screen.queryByRole('columnheader', { name: 'Weighted Share' })).not.toBeInTheDocument();
    unmount();

    renderCohort({
      clusterQueues: [
        cohortQueue('cq-a', { status: { fairSharing: { weightedShare: 50 } } }),
        cohortQueue('cq-b'),
      ],
    });

    expect(screen.getByRole('columnheader', { name: 'Weighted Share' })).toBeInTheDocument();
    expect(screen.getByText('50')).toBeInTheDocument();
    // The queue without fair sharing data shows a dash. Its link also appears
    // in the settings table, which is the last one on the page.
    const usageRow = screen.getAllByRole('link', { name: 'cq-b' })[0].closest('tr');
    expect(within(usageRow).getByText('-')).toBeInTheDocument();
  });

  it('lists the preemption and fungibility settings of each queue', () => {
    renderCohort({ clusterQueues: [cohortQueue('cq-a')] });

    const row = screen.getByText('BestEffortFIFO').closest('tr');
    expect(
      within(row).getAllByRole('cell').map((cell) => cell.textContent)
    ).toEqual(['cq-a', 'TryNextFlavor', 'MayStopSearch', 'LowerPriority', 'Any', 'Never', 'BestEffortFIFO']);
  });

  it('shows N/A for settings a queue does not configure', () => {
    renderCohort({ clusterQueues: [{ name: 'cq-a', spec: {} }] });

    // The queue is also listed in the usage table, so take the settings row.
    const row = screen.getAllByRole('link', { name: 'cq-a' }).at(-1).closest('tr');
    expect(within(row).getAllByText('N/A')).toHaveLength(6);
  });

  it('shows a message and no utilization for a cohort without queues', () => {
    renderCohort({ clusterQueues: [] });

    expect(screen.getByText('No cluster queues are part of this cohort.')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Cohort Resource Utilization' })).not.toBeInTheDocument();
  });
});
