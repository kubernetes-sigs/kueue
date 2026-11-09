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
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import Dashboard from './Dashboard';
import * as useWebSocketModule from './useWebSocket';

vi.mock('./useWebSocket', () => ({
  default: vi.fn(),
}));

afterEach(cleanup);

const makeWorkload = (namespace, podName) => ({
  metadata: { namespace, name: 'wl', uid: `${namespace}-uid` },
  spec: { queueName: 'lq', podSets: [{ count: 1 }] },
  status: { conditions: [] },
  pods: [{ name: podName, status: { phase: 'Running' } }],
});

describe('Dashboard workload rows', () => {
  it('expands only the clicked workload when two namespaces use the same name', () => {
    // Return the same objects on every render, as the real hook does.
    const namespaces = { data: { namespaces: ['ns-a', 'ns-b'] }, error: null };
    const dashboard = {
      data: {
        queues: [],
        clusterQueues: [],
        workloads: {
          items: [makeWorkload('ns-a', 'pod-in-a'), makeWorkload('ns-b', 'pod-in-b')],
          workloads_by_uid: {},
        },
      },
      error: null,
    };
    vi.mocked(useWebSocketModule.default).mockImplementation((url) =>
      url === '/ws/namespaces' ? namespaces : dashboard
    );

    render(
      <MemoryRouter>
        <Dashboard />
      </MemoryRouter>
    );

    const rowA = screen.getByText('ns-a').closest('tr');
    fireEvent.click(rowA.querySelector('button'));

    expect(screen.getByText('pod-in-a')).toBeInTheDocument();
    expect(screen.queryByText('pod-in-b')).not.toBeInTheDocument();
  });
});
