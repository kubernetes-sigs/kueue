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
import WorkloadDetail from './WorkloadDetail';
import ClusterQueueDetail from './ClusterQueueDetail';
import * as useWebSocketModule from './useWebSocket';

vi.mock('./useWebSocket', () => ({
  default: vi.fn(),
}));

afterEach(cleanup);

const renderWorkloadDetail = (clusterQueueName) => {
  // Return the same objects on every render, as the real hook does, so the
  // effect that copies events into state does not loop.
  const events = { data: [], error: null };
  const workload = {
    data: {
      metadata: { name: 'wl-1', namespace: 'ns-1' },
      spec: { queueName: 'lq-1' },
      status: {},
      clusterQueueName,
    },
    error: null,
  };
  vi.mocked(useWebSocketModule.default).mockImplementation((url) =>
    url.endsWith('/events') ? events : workload
  );
  return render(
    <MemoryRouter initialEntries={['/workload/ns-1/wl-1']}>
      <Routes>
        <Route path="/workload/:namespace/:workloadName" element={<WorkloadDetail />} />
      </Routes>
    </MemoryRouter>
  );
};

const renderClusterQueueDetail = (cohortName) => {
  vi.mocked(useWebSocketModule.default).mockReturnValue({
    data: {
      metadata: { name: 'cq-a' },
      spec: { cohortName, resourceGroups: [] },
      status: {},
      queues: [],
    },
    error: null,
  });
  return render(
    <MemoryRouter initialEntries={['/cluster-queue/cq-a']}>
      <Routes>
        <Route path="/cluster-queue/:clusterQueueName" element={<ClusterQueueDetail />} />
      </Routes>
    </MemoryRouter>
  );
};

describe('WorkloadDetail ClusterQueue link', () => {
  it('links to the ClusterQueue when the name is known', () => {
    renderWorkloadDetail('cq-a');
    expect(screen.getByRole('link', { name: 'cq-a' })).toHaveAttribute('href', '/cluster-queue/cq-a');
  });

  it('shows Unknown without a link when the ClusterQueue is unknown', () => {
    renderWorkloadDetail('Unknown');
    expect(screen.getByText(/Unknown/)).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Unknown' })).not.toBeInTheDocument();
  });
});

describe('ClusterQueueDetail Cohort link', () => {
  it('links to the Cohort when the ClusterQueue has one', () => {
    renderClusterQueueDetail('team');
    expect(screen.getByRole('link', { name: 'team' })).toHaveAttribute('href', '/cohort/team');
  });

  it('renders no Cohort link when the ClusterQueue has no Cohort', () => {
    renderClusterQueueDetail(undefined);
    expect(screen.queryAllByRole('link').filter((l) => l.getAttribute('href').startsWith('/cohort/'))).toHaveLength(0);
  });
});
