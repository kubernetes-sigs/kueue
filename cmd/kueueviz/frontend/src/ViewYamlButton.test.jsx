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
import ViewYamlButton from './ViewYamlButton';

// YamlViewer fetches over HTTP and embeds Ace; it has its own tests.
vi.mock('./YamlViewer', () => ({
  default: ({ open, onClose, resourceType, resourceName, namespace }) =>
    open ? (
      <div role="dialog">
        <span>{`${resourceType}|${resourceName}|${namespace}`}</span>
        <button onClick={onClose}>close</button>
      </div>
    ) : null,
}));

afterEach(cleanup);

describe('ViewYamlButton', () => {
  it('renders the button with the viewer closed', () => {
    render(<ViewYamlButton resourceType="workload" resourceName="wl-1" />);

    expect(screen.getByRole('button', { name: 'View YAML' })).toBeInTheDocument();
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('opens the viewer for the given resource and namespace', () => {
    render(<ViewYamlButton resourceType="workload" resourceName="wl-1" namespace="ns-1" />);

    fireEvent.click(screen.getByRole('button', { name: 'View YAML' }));

    expect(screen.getByRole('dialog')).toHaveTextContent('workload|wl-1|ns-1');
  });

  it('defaults the namespace to null for cluster-scoped resources', () => {
    render(<ViewYamlButton resourceType="clusterqueue" resourceName="cq-1" />);

    fireEvent.click(screen.getByRole('button', { name: 'View YAML' }));

    expect(screen.getByRole('dialog')).toHaveTextContent('clusterqueue|cq-1|null');
  });

  it('closes the viewer again', () => {
    render(<ViewYamlButton resourceType="cohort" resourceName="co-1" />);

    fireEvent.click(screen.getByRole('button', { name: 'View YAML' }));
    fireEvent.click(screen.getByRole('button', { name: 'close' }));

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('does not propagate the click to parent handlers', () => {
    const onParentClick = vi.fn();
    render(
      <div onClick={onParentClick}>
        <ViewYamlButton resourceType="cohort" resourceName="co-1" />
      </div>
    );

    fireEvent.click(screen.getByRole('button', { name: 'View YAML' }));

    expect(onParentClick).not.toHaveBeenCalled();
  });
});
