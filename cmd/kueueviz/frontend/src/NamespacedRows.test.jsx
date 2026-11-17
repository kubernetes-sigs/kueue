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
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import Workloads from './Workloads';
import LocalQueues from './LocalQueues';
import useWebSocket from './useWebSocket';

vi.mock('./useWebSocket', () => ({
  default: vi.fn(),
}));

vi.mock('./YamlViewer', () => ({
  default: ({ open, namespace, resourceName }) =>
    open ? <div role="dialog">{namespace}/{resourceName}</div> : null,
}));

afterEach(cleanup);

describe.each([
  {
    name: 'Workloads',
    Component: Workloads,
    dataForNamespaces: (namespaces) => ({
      workloads: {
        items: namespaces.map((namespace) => ({
          metadata: { namespace, name: 'shared' },
          spec: { queueName: 'queue' },
          status: {},
        })),
      },
    }),
  },
  {
    name: 'LocalQueues',
    Component: LocalQueues,
    dataForNamespaces: (namespaces) => namespaces.map((namespace) => ({
      namespace,
      name: 'shared',
      spec: { clusterQueue: 'cluster-queue' },
      status: {},
    })),
  },
])('$name namespace row identity', ({ Component, dataForNamespaces }) => {
  it.each([
    {
      name: 'keeps the YAML dialog on its resource when a namespace group is inserted',
      selectedNamespace: 'team-c',
      updatedNamespaces: ['team-a', 'team-b', 'team-c'],
      expectedNamespace: 'team-c',
    },
    {
      name: 'closes the YAML dialog when its resource and namespace group are removed',
      selectedNamespace: 'team-a',
      updatedNamespaces: ['team-c'],
      expectedNamespace: null,
    },
  ])('$name', ({ selectedNamespace, updatedNamespaces, expectedNamespace }) => {
    const namespaces = { namespaces: ['team-a', 'team-b', 'team-c'] };
    let data = dataForNamespaces(['team-a', 'team-c']);
    vi.mocked(useWebSocket).mockImplementation((url) => ({
      data: url === '/ws/namespaces' ? namespaces : data,
      error: null,
    }));

    const page = (
      <MemoryRouter>
        <Component />
      </MemoryRouter>
    );
    const { rerender } = render(page);
    const row = screen.getByText(selectedNamespace).closest('tr');
    fireEvent.click(within(row).getByRole('button', { name: 'View YAML' }));
    expect(screen.getByRole('dialog')).toHaveTextContent(`${selectedNamespace}/shared`);

    data = dataForNamespaces(updatedNamespaces);
    rerender(
      <MemoryRouter>
        <Component />
      </MemoryRouter>
    );

    if (expectedNamespace) {
      expect(screen.getAllByRole('dialog')).toHaveLength(1);
      expect(screen.getByRole('dialog')).toHaveTextContent(`${expectedNamespace}/shared`);
    } else {
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    }
  });
});
