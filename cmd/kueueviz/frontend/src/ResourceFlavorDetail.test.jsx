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
import ResourceFlavorDetail from './ResourceFlavorDetail';
import { mockWebSocket, renderAtRoute } from './testing/helpers';

vi.mock('./useWebSocket', () => ({
  default: vi.fn(),
}));

afterEach(cleanup);

const FLAVOR_URL = '/ws/resource-flavor/spot';

const flavor = (overrides = {}) => ({
  name: 'spot',
  details: {
    nodeLabels: { pool: 'spot', zone: 'a' },
    nodeTaints: [{ key: 'spot', value: 'true', effect: 'NoSchedule' }],
    tolerations: [{ key: 'spot', operator: 'Exists', tolerationSeconds: 30 }],
  },
  queues: [
    { queueName: 'cq-b', quota: [{ resource: 'cpu', nominalQuota: '4' }, { resource: 'memory', nominalQuota: '8Gi' }] },
    { queueName: 'cq-a', quota: [{ resource: 'cpu', nominalQuota: '2' }] },
  ],
  nodes: [
    { name: 'node-b', labels: { pool: 'spot' }, taints: [] },
    { name: 'node-a', labels: { zone: 'a' }, taints: [{ key: 'spot', effect: 'NoSchedule' }] },
    { name: 'node-c', labels: {}, taints: [] },
  ],
  ...overrides,
});

const renderFlavor = (response) => {
  mockWebSocket({ [FLAVOR_URL]: response });
  return renderAtRoute(<ResourceFlavorDetail />, '/resource-flavor/:flavorName', '/resource-flavor/spot');
};

describe('ResourceFlavorDetail page', () => {
  it('omits taint and toleration fields the API leaves empty instead of showing undefined', () => {
    renderFlavor({
      data: {
        name: 'spot',
        details: {
          nodeLabels: {},
          nodeTaints: [{ key: 'spot', effect: 'NoSchedule' }],
          tolerations: [{ operator: 'Exists' }],
        },
        queues: [],
        nodes: [{ name: 'node-1', labels: {}, taints: [{ key: 'spot', value: '', effect: 'NoExecute' }] }],
      },
      error: null,
    });

    expect(screen.getByText('spot:NoSchedule')).toBeInTheDocument();
    expect(screen.getByText('op=Exists')).toBeInTheDocument();
    expect(screen.getByText('spot:NoExecute')).toBeInTheDocument();
    expect(screen.queryByText(/undefined/)).not.toBeInTheDocument();
  });

  it('shows a loading state until the flavor arrives', () => {
    renderFlavor({ data: null, error: null });

    expect(screen.getByText('Loading...')).toBeInTheDocument();
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('keeps loading for a payload without a name', () => {
    renderFlavor({ data: {}, error: null });

    expect(screen.getByText('Loading...')).toBeInTheDocument();
  });

  it('shows the error instead of the page', () => {
    renderFlavor({ data: null, error: 'WebSocket connection failed.' });

    expect(screen.getByText('WebSocket connection failed.')).toBeInTheDocument();
    expect(screen.queryByText('Loading...')).not.toBeInTheDocument();
  });

  it('renders the node labels, taints and tolerations', () => {
    renderFlavor({ data: flavor(), error: null });

    expect(screen.getByRole('heading', { name: 'Resource Flavor Detail: spot' })).toBeInTheDocument();
    expect(screen.getByText('pool: spot')).toBeInTheDocument();
    expect(screen.getByText('zone: a')).toBeInTheDocument();
    expect(screen.getAllByText('spot=true:NoSchedule')).toHaveLength(1);
    expect(screen.getByText('spot op=Exists for 30s')).toBeInTheDocument();
  });

  it('shows "None" for empty labels, taints and tolerations', () => {
    renderFlavor({ data: flavor({ details: { nodeLabels: {} } }), error: null });

    expect(screen.getAllByText('None')).toHaveLength(3);
  });

  it('lists the queues sorted by name, with one row per resource', () => {
    renderFlavor({ data: flavor(), error: null });

    const links = screen.getAllByRole('link');
    expect(links.map((link) => link.textContent)).toEqual(['cq-a', 'cq-b']);
    expect(links[1]).toHaveAttribute('href', '/cluster-queue/cq-b');
    const rowOfB = screen.getByRole('link', { name: 'cq-b' }).closest('tr');
    expect(rowOfB).toHaveTextContent('cpu4');
    expect(screen.getByRole('link', { name: 'cq-b' }).closest('td')).toHaveAttribute('rowspan', '2');
    expect(screen.getByText('memory').closest('tr')).toHaveTextContent('8Gi');
  });

  it('shows empty messages without queues and nodes', () => {
    renderFlavor({ data: flavor({ queues: [], nodes: [] }), error: null });

    expect(screen.getByText('No cluster queues are using this flavor.')).toBeInTheDocument();
    expect(screen.getByText('No nodes match this flavor.')).toBeInTheDocument();
  });

  it('tolerates a flavor without queues and nodes keys', () => {
    renderFlavor({ data: { name: 'spot', details: {} }, error: null });

    expect(screen.getByText('No cluster queues are using this flavor.')).toBeInTheDocument();
    expect(screen.getByText('No nodes match this flavor.')).toBeInTheDocument();
  });

  it('lists the nodes sorted by name with their taints', () => {
    renderFlavor({ data: flavor(), error: null });

    const rows = within(screen.getByText('Node Name').closest('table')).getAllByRole('row').slice(1);
    expect(rows.map((row) => within(row).getAllByRole('cell')[0].textContent)).toEqual(['node-a', 'node-b', 'node-c']);
    expect(within(rows[0]).getByText('spot:NoSchedule')).toBeInTheDocument();
    // A node without taints shows a dash.
    expect(within(rows[1]).getByText('—')).toBeInTheDocument();
  });

  it('shows the count of queues and nodes next to their headings', () => {
    renderFlavor({ data: flavor(), error: null });

    // The fixture has 2 queues and 3 nodes, so a swapped count is detected.
    expect(screen.getByRole('heading', { name: 'Queues Using This Flavor 2' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Nodes Matching This Flavor 3' })).toBeInTheDocument();
  });

  it('shows the node labels only on request', () => {
    renderFlavor({ data: flavor(), error: null });
    const nodeTable = () => screen.getByText('Node Name').closest('table');
    expect(within(nodeTable()).queryByRole('columnheader', { name: 'Labels' })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('switch', { name: 'Show Labels' }));

    expect(within(nodeTable()).getByRole('columnheader', { name: 'Labels' })).toBeInTheDocument();
    expect(within(nodeTable()).getByText('zone: a')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('switch', { name: 'Show Labels' }));
    expect(within(nodeTable()).queryByRole('columnheader', { name: 'Labels' })).not.toBeInTheDocument();
  });
});
