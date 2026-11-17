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
import ResourceFlavorDetail from './ResourceFlavorDetail';
import useWebSocket from './useWebSocket';

vi.mock('./useWebSocket', () => ({
  default: vi.fn(),
}));

afterEach(cleanup);

describe('ResourceFlavorDetail taints and tolerations', () => {
  it('omits fields the API leaves empty instead of showing undefined', () => {
    vi.mocked(useWebSocket).mockReturnValue({
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

    render(
      <MemoryRouter initialEntries={['/resource-flavor/spot']}>
        <Routes>
          <Route path="/resource-flavor/:flavorName" element={<ResourceFlavorDetail />} />
        </Routes>
      </MemoryRouter>
    );

    expect(screen.getByText('spot:NoSchedule')).toBeInTheDocument();
    expect(screen.getByText('op=Exists')).toBeInTheDocument();
    expect(screen.getByText('spot:NoExecute')).toBeInTheDocument();
    expect(screen.queryByText(/undefined/)).not.toBeInTheDocument();
  });
});
