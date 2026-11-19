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
import Workloads from './Workloads';
import * as useWebSocketModule from './useWebSocket';

vi.mock('./useWebSocket', () => ({
  default: vi.fn(),
}));

// ViewYamlButton needs an AuthProvider and is not under test here.
vi.mock('./ViewYamlButton', () => ({
  default: () => null,
}));

afterEach(cleanup);

describe('Workloads owner reference tooltip', () => {
  it('shows the owner reference from metadata', async () => {
    // Return the same objects on every render, as the real hook does.
    const namespaces = { data: { namespaces: ['ns-1'] }, error: null };
    const workloads = {
      data: {
        workloads: {
          items: [
            {
              metadata: {
                name: 'wl-1',
                namespace: 'ns-1',
                ownerReferences: [{ apiVersion: 'batch/v1', kind: 'Job', name: 'job-1', uid: 'job-uid-1' }],
              },
              spec: { queueName: 'lq-1', podSets: [{ count: 1 }] },
              status: {},
            },
          ],
        },
      },
      error: null,
    };
    vi.mocked(useWebSocketModule.default).mockImplementation((url) =>
      url === '/ws/namespaces' ? namespaces : workloads
    );

    render(
      <MemoryRouter>
        <Workloads />
      </MemoryRouter>
    );
    fireEvent.mouseOver(screen.getByText('wl-1'));

    expect(await screen.findByText('Owner Reference: job-uid-1')).toBeInTheDocument();
    expect(screen.getByText('API Version: batch/v1')).toBeInTheDocument();
    expect(screen.getByText('Kind: Job')).toBeInTheDocument();
    expect(screen.getByText('Name: job-1')).toBeInTheDocument();
  });
});
