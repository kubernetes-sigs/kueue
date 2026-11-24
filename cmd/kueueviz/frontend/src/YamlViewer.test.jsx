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
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import YamlViewer from './YamlViewer';
import { useAuthFetch } from './AuthContext';

const authFetch = vi.fn();

vi.mock('./AuthContext', () => ({
  useAuthFetch: vi.fn(),
}));

vi.mock('./utils/urlHelper', () => ({
  buildResourceUrl: (type, name, { namespace, output }) =>
    `http://backend/api/${type}/${name}?namespace=${namespace}&output=${output}`,
}));

// Ace needs a real browser layout engine; render its value instead.
vi.mock('react-ace', () => ({
  default: ({ value, readOnly, mode }) => (
    <pre data-testid="ace" data-readonly={String(readOnly)} data-mode={mode}>
      {value}
    </pre>
  ),
}));
vi.mock('ace-builds/src-noconflict/mode-yaml', () => ({}));
vi.mock('ace-builds/src-noconflict/ext-searchbox', () => ({}));

const okResponse = (content) => ({
  ok: true,
  json: () => Promise.resolve({ content }),
});

const renderViewer = (props = {}) => {
  const onClose = vi.fn();
  render(
    <YamlViewer open onClose={onClose} resourceType="workload" resourceName="wl-1" namespace="ns-1" {...props} />
  );
  return onClose;
};

beforeEach(() => {
  vi.mocked(useAuthFetch).mockReturnValue(authFetch);
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('YamlViewer', () => {
  it('titles the dialog with the capitalized type, name and namespace', async () => {
    authFetch.mockResolvedValue(okResponse('kind: Workload'));

    renderViewer();

    expect(await screen.findByText('Workload: wl-1 (ns-1)')).toBeInTheDocument();
  });

  it('omits the namespace from the title for cluster-scoped resources', async () => {
    authFetch.mockResolvedValue(okResponse('kind: ClusterQueue'));

    renderViewer({ resourceType: 'clusterqueue', resourceName: 'cq-1', namespace: null });

    expect(await screen.findByText('Clusterqueue: cq-1')).toBeInTheDocument();
  });

  it('fetches the YAML of the resource and shows it read-only', async () => {
    authFetch.mockResolvedValue(okResponse('kind: Workload\nmetadata:\n  name: wl-1'));

    renderViewer();

    const editor = await screen.findByTestId('ace');
    expect(authFetch).toHaveBeenCalledExactlyOnceWith('http://backend/api/workload/wl-1?namespace=ns-1&output=yaml');
    expect(editor).toHaveTextContent('kind: Workload');
    expect(editor).toHaveAttribute('data-readonly', 'true');
    expect(editor).toHaveAttribute('data-mode', 'yaml');
  });

  it('shows a spinner while loading', async () => {
    let resolve;
    authFetch.mockReturnValue(new Promise((r) => { resolve = r; }));

    renderViewer();

    expect(await screen.findByRole('progressbar')).toBeInTheDocument();
    expect(screen.queryByTestId('ace')).not.toBeInTheDocument();

    resolve(okResponse('kind: Workload'));
    expect(await screen.findByTestId('ace')).toBeInTheDocument();
    expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
  });

  it('shows the HTTP status when the backend answers with an error', async () => {
    authFetch.mockResolvedValue({ ok: false, status: 404, statusText: 'Not Found' });

    renderViewer();

    expect(await screen.findByText('Error: Failed to fetch YAML: 404 Not Found')).toBeInTheDocument();
    expect(screen.queryByTestId('ace')).not.toBeInTheDocument();
  });

  it('shows the error when the request itself fails', async () => {
    authFetch.mockRejectedValue(new Error('Authentication required'));

    renderViewer();

    expect(await screen.findByText('Error: Authentication required')).toBeInTheDocument();
  });

  it('does not fetch while closed', () => {
    renderViewer({ open: false });

    expect(authFetch).not.toHaveBeenCalled();
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('fetches once it is opened', async () => {
    authFetch.mockResolvedValue(okResponse('kind: Workload'));
    const props = { onClose: vi.fn(), resourceType: 'workload', resourceName: 'wl-1', namespace: 'ns-1' };
    const { rerender } = render(<YamlViewer open={false} {...props} />);

    rerender(<YamlViewer open {...props} />);

    expect(await screen.findByTestId('ace')).toBeInTheDocument();
    expect(authFetch).toHaveBeenCalledTimes(1);
  });

  it('closes from the close button', async () => {
    authFetch.mockResolvedValue(okResponse('kind: Workload'));
    const onClose = renderViewer();
    await screen.findByTestId('ace');

    // The only button in the dialog is the close icon.
    fireEvent.click(screen.getByRole('button'));

    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('closes on Escape', async () => {
    authFetch.mockResolvedValue(okResponse('kind: Workload'));
    const onClose = renderViewer();
    await screen.findByTestId('ace');

    fireEvent.keyDown(document, { key: 'Escape' });

    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it('stays open for other key presses', async () => {
    authFetch.mockResolvedValue(okResponse('kind: Workload'));
    const onClose = renderViewer();
    await screen.findByTestId('ace');

    fireEvent.keyDown(document, { key: 'f', ctrlKey: true });
    fireEvent.keyDown(document, { key: 'a' });

    expect(onClose).not.toHaveBeenCalled();
  });
});
