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
import Workloads from './Workloads';
import useWebSocket from './useWebSocket';
import { mockWebSocket, renderWithRouter } from './testing/helpers';

vi.mock('./useWebSocket', () => ({
  default: vi.fn(),
}));

// ViewYamlButton needs an AuthProvider and is not under test here.
vi.mock('./ViewYamlButton', () => ({
  default: ({ resourceType, resourceName, namespace }) => (
    <span>{`yaml:${resourceType}:${namespace}/${resourceName}`}</span>
  ),
}));

afterEach(cleanup);

const namespaceList = { data: { namespaces: ['ns-1', 'ns-2'] }, error: null };

const wl = (namespace, name, extra = {}) => ({
  metadata: { namespace, name },
  spec: { queueName: 'lq', priority: 10, podSets: [{ count: 2 }, { count: 3 }] },
  status: {},
  ...extra,
});

const renderPage = (workloads, extra = {}) => {
  mockWebSocket({
    '/ws/namespaces': namespaceList,
    '/ws/workloads': workloads,
    ...extra,
  });
  return renderWithRouter(<Workloads />);
};

describe('Workloads', () => {
  it('shows the owner reference from metadata in a tooltip', async () => {
    renderPage({
      data: {
        workloads: {
          items: [
            wl('ns-1', 'wl-1', {
              metadata: {
                name: 'wl-1',
                namespace: 'ns-1',
                ownerReferences: [{ apiVersion: 'batch/v1', kind: 'Job', name: 'job-1', uid: 'job-uid-1' }],
              },
            }),
          ],
        },
      },
      error: null,
    });
    fireEvent.mouseOver(screen.getByText('wl-1'));

    expect(await screen.findByText('Owner Reference: job-uid-1')).toBeInTheDocument();
    expect(screen.getByText('API Version: batch/v1')).toBeInTheDocument();
    expect(screen.getByText('Kind: Job')).toBeInTheDocument();
    expect(screen.getByText('Name: job-1')).toBeInTheDocument();
  });

  it('shows a spinner until the first message arrives', () => {
    renderPage({ data: null, error: null });

    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it.each([
    ['workloads', { data: null, error: 'workloads failed' }, undefined, 'workloads failed'],
    ['namespaces', { data: null, error: null }, { '/ws/namespaces': { data: null, error: 'ns failed' } }, 'ns failed'],
  ])('shows the %s error but keeps the filter', (_, workloads, extra, message) => {
    renderPage(workloads, extra);

    expect(screen.getByText(message)).toBeInTheDocument();
    expect(screen.getByRole('combobox')).toBeInTheDocument();
  });

  it('shows an empty message for all namespaces', () => {
    renderPage({ data: { workloads: { items: [] } }, error: null });

    expect(screen.getByText('No workloads found in any namespace.')).toBeInTheDocument();
  });

  it('renders the details of a workload', () => {
    renderPage({
      data: {
        workloads: {
          items: [
            wl('ns-1', 'wl-1', {
              status: { conditions: [{ type: 'Admitted', status: 'True' }] },
            }),
          ],
        },
      },
      error: null,
    });

    const row = screen.getByRole('link', { name: 'wl-1' }).closest('tr');
    expect(screen.getByRole('link', { name: 'wl-1' })).toHaveAttribute('href', '/workload/ns-1/wl-1');
    expect(within(row).getByRole('link', { name: 'lq' })).toHaveAttribute('href', '/local-queue/ns-1/lq');
    ['Admitted', '10'].forEach((text) =>
      expect(within(row).getByText(text)).toBeInTheDocument()
    );
    expect(within(row).getByText('yaml:workload:ns-1/wl-1')).toBeInTheDocument();
  });

  it('shows "No" and "N/A" for a workload that was not preempted', () => {
    renderPage({ data: { workloads: { items: [wl('ns-1', 'wl-1')] } }, error: null });

    const row = screen.getByRole('link', { name: 'wl-1' }).closest('tr');
    expect(within(row).getByText('No')).toBeInTheDocument();
    expect(within(row).getByText('N/A')).toBeInTheDocument();
    expect(within(row).getByText('Pending')).toBeInTheDocument();
  });

  it('groups workloads by namespace under one spanning cell', () => {
    renderPage({
      data: { workloads: { items: [wl('ns-1', 'a'), wl('ns-2', 'b'), wl('ns-1', 'c')] } },
      error: null,
    });

    const order = screen
      .getAllByRole('link', { name: /^[abc]$/ })
      .map((link) => link.textContent);
    expect(order).toEqual(['a', 'c', 'b']);
    expect(screen.getAllByText('ns-1')).toHaveLength(1);
    expect(screen.getByText('ns-1').closest('td')).toHaveAttribute('rowspan', '2');
    expect(screen.getByText('ns-2').closest('td')).toHaveAttribute('rowspan', '1');
  });

  it('offers every namespace and requests the selected one', () => {
    renderPage({ data: { workloads: { items: [wl('ns-1', 'a')] } }, error: null });

    fireEvent.mouseDown(screen.getByRole('combobox'));
    expect(screen.getAllByRole('option').map((option) => option.textContent)).toEqual([
      'All Namespaces',
      'ns-1',
      'ns-2',
    ]);
    fireEvent.click(screen.getByRole('option', { name: 'ns-2' }));

    expect(vi.mocked(useWebSocket)).toHaveBeenLastCalledWith('/ws/workloads?namespace=ns-2');
  });

  it('names the namespace in the empty message of a filtered view', () => {
    renderPage(
      { data: { workloads: { items: [wl('ns-1', 'a')] } }, error: null },
      { '/ws/workloads?namespace=ns-2': { data: { workloads: { items: [] } }, error: null } }
    );

    fireEvent.mouseDown(screen.getByRole('combobox'));
    fireEvent.click(screen.getByRole('option', { name: 'ns-2' }));

    expect(screen.getByText(/No workloads found in namespace "ns-2"/)).toBeInTheDocument();
  });
});
