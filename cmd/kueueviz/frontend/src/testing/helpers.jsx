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

import React from 'react';
import { render } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { vi } from 'vitest';
import useWebSocket from '../useWebSocket';

/**
 * Shared helpers for the component tests.
 *
 * A test file using them must mock the hook itself, because vi.mock is hoisted
 * per file:
 *
 *   vi.mock('../useWebSocket', () => ({ default: vi.fn() }));
 */

/**
 * Makes useWebSocket answer from a map of URL to response. A response is
 * { data, error }; unknown URLs behave like a connection that has not
 * delivered anything yet. Responses are returned by reference, as the real hook
 * does, so effects depending on `data` do not re-run on every render.
 */
export const mockWebSocket = (responses) => {
  const loading = { data: null, error: null };
  vi.mocked(useWebSocket).mockImplementation((url) => responses[url] ?? loading);
};

/** Renders a component without route params inside a router. */
export const renderWithRouter = (ui, initialEntries = ['/']) =>
  render(<MemoryRouter initialEntries={initialEntries}>{ui}</MemoryRouter>);

/** Renders a component at `path`, resolved against `url`, so useParams works. */
export const renderAtRoute = (ui, path, url) =>
  render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route path={path} element={ui} />
      </Routes>
    </MemoryRouter>
  );
