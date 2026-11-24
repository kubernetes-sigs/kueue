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

import { afterEach, beforeEach, describe, it, expect, vi } from 'vitest';
import '@testing-library/jest-dom/vitest';
import React from 'react';
import { cleanup, render, screen } from '@testing-library/react';
import App from './App';

// Every page is covered by its own tests. Replace them with markers so that
// this file only verifies the routing and the authentication guard.
vi.mock('./Dashboard', () => ({ default: () => <div>page:dashboard</div> }));
vi.mock('./Workloads', () => ({ default: () => <div>page:workloads</div> }));
vi.mock('./LocalQueues', () => ({ default: () => <div>page:local-queues</div> }));
vi.mock('./ClusterQueues', () => ({ default: () => <div>page:cluster-queues</div> }));
vi.mock('./ResourceFlavors', () => ({ default: () => <div>page:resource-flavors</div> }));
vi.mock('./Cohorts', () => ({ default: () => <div>page:cohorts</div> }));
vi.mock('./WorkloadDetail', () => ({ default: () => <div>page:workload-detail</div> }));
vi.mock('./LocalQueueDetail', () => ({ default: () => <div>page:local-queue-detail</div> }));
vi.mock('./ClusterQueueDetail', () => ({ default: () => <div>page:cluster-queue-detail</div> }));
vi.mock('./ResourceFlavorDetail', () => ({ default: () => <div>page:resource-flavor-detail</div> }));
vi.mock('./CohortDetail', () => ({ default: () => <div>page:cohort-detail</div> }));

vi.mock('./utils/urlHelper', () => ({ getBackendHttpUrl: () => 'http://backend' }));

const mockAuthMode = (authMode) => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ json: () => Promise.resolve({ authMode }) }));
};

const renderAt = (path) => {
  window.history.pushState({}, '', path);
  return render(<App />);
};

beforeEach(() => {
  sessionStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('App routes without authentication', () => {
  beforeEach(() => mockAuthMode('Disabled'));

  it.each([
    ['/', 'page:dashboard'],
    ['/workloads', 'page:workloads'],
    ['/local-queues', 'page:local-queues'],
    ['/cluster-queues', 'page:cluster-queues'],
    ['/resource-flavors', 'page:resource-flavors'],
    ['/cohorts', 'page:cohorts'],
    ['/workload/ns/wl', 'page:workload-detail'],
    ['/local-queue/ns/lq', 'page:local-queue-detail'],
    ['/cluster-queue/cq', 'page:cluster-queue-detail'],
    ['/resource-flavor/rf', 'page:resource-flavor-detail'],
    ['/cohort/co', 'page:cohort-detail'],
  ])('routes %s to %s', async (path, page) => {
    renderAt(path);

    expect(await screen.findByText(page)).toBeInTheDocument();
  });

  it('shows the navigation bar on every page', async () => {
    renderAt('/workloads');

    await screen.findByText('page:workloads');
    expect(screen.getByRole('img', { name: 'KueueViz Logo' })).toBeInTheDocument();
  });

  it('renders nothing for an unknown path', async () => {
    renderAt('/no/such/page');

    expect(await screen.findByRole('img', { name: 'KueueViz Logo' })).toBeInTheDocument();
    expect(screen.queryByText(/^page:/)).not.toBeInTheDocument();
  });
});

describe('App routes with authentication', () => {
  beforeEach(() => mockAuthMode('Required'));

  it('shows the login page without requiring a token', async () => {
    renderAt('/login');

    expect(await screen.findByText('KueueViz Authentication')).toBeInTheDocument();
  });

  it.each(['/', '/workloads', '/cohort/co'])('redirects %s to the login page without a token', async (path) => {
    renderAt(path);

    expect(await screen.findByText('KueueViz Authentication')).toBeInTheDocument();
    expect(window.location.pathname).toBe('/login');
    expect(screen.queryByText(/^page:/)).not.toBeInTheDocument();
  });

  it('shows the page and the logout button with a token', async () => {
    sessionStorage.setItem('kueueviz_token', 'tok');

    renderAt('/workloads');

    expect(await screen.findByText('page:workloads')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Logout' })).toBeInTheDocument();
  });
});
