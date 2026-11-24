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
import ResourceFlavors from './ResourceFlavors';
import { mockWebSocket, renderWithRouter } from './testing/helpers';

vi.mock('./useWebSocket', () => ({ default: vi.fn() }));

// ViewYamlButton needs an AuthProvider and is covered by its own tests.
vi.mock('./ViewYamlButton', () => ({
  default: ({ resourceType, resourceName }) => <span>{`yaml:${resourceType}/${resourceName}`}</span>,
}));

afterEach(cleanup);

const render = (response) => {
  mockWebSocket({ '/ws/resource-flavors': response });
  return renderWithRouter(<ResourceFlavors />);
};

describe('ResourceFlavors', () => {
  it('shows a spinner until the first message arrives', () => {
    render({ data: null, error: null });

    expect(screen.getByRole('progressbar')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Resource Flavors' })).toBeInTheDocument();
  });

  it('shows the error instead of the page', () => {
    render({ data: null, error: 'WebSocket connection failed.' });

    expect(screen.getByText('WebSocket connection failed.')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Resource Flavors' })).not.toBeInTheDocument();
  });

  it.each([[[]], [{ not: 'an array' }]])('shows an empty message for %j', (data) => {
    render({ data, error: null });

    expect(screen.getByText('No Resource Flavors found.')).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('lists the flavors sorted by name, linking to their detail page', () => {
    render({
      data: [
        { name: 'spot', details: { nodeLabels: { pool: 'spot' } } },
        { name: 'on-demand', details: {} },
        { details: {}, name: 'gpu' },
      ],
      error: null,
    });

    const links = screen.getAllByRole('link');
    expect(links.map((link) => link.textContent)).toEqual(['gpu', 'on-demand', 'spot']);
    expect(links.map((link) => link.getAttribute('href'))).toEqual([
      '/resource-flavor/gpu',
      '/resource-flavor/on-demand',
      '/resource-flavor/spot',
    ]);
  });

  it('shows the details as JSON and a YAML button per flavor', () => {
    render({ data: [{ name: 'spot', details: { nodeLabels: { pool: 'spot' } } }], error: null });

    const row = screen.getByRole('link', { name: 'spot' }).closest('tr');
    expect(within(row).getByText('{"nodeLabels":{"pool":"spot"}}')).toBeInTheDocument();
    expect(within(row).getByText('yaml:resourceflavor/spot')).toBeInTheDocument();
  });

  it('does not mutate the data it received while sorting', () => {
    const data = [{ name: 'b', details: {} }, { name: 'a', details: {} }];
    render({ data, error: null });

    expect(data.map((flavor) => flavor.name)).toEqual(['b', 'a']);
  });
});
