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
import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react';
import Cohorts, { buildCohortTree } from './Cohorts';
import { mockWebSocket, renderWithRouter } from './testing/helpers';

vi.mock('./useWebSocket', () => ({ default: vi.fn() }));

vi.mock('./ViewYamlButton', () => ({
  default: ({ resourceName }) => <span>{`yaml:${resourceName}`}</span>,
}));

afterEach(cleanup);

const render = (response) => {
  mockWebSocket({ '/ws/cohorts': response });
  return renderWithRouter(<Cohorts />);
};

const cohorts = [
  { name: 'child', parentName: 'root', clusterQueues: [{ name: 'cq-1' }, { name: 'cq-2' }] },
  { name: 'root', clusterQueues: [] },
  { name: 'alone', clusterQueues: [{ name: 'cq-3' }] },
];

describe('Cohorts', () => {
  it('shows a spinner until the first message arrives', () => {
    render({ data: null, error: null });

    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('shows the error instead of the page', () => {
    render({ data: null, error: 'WebSocket connection failed.' });

    expect(screen.getByText('WebSocket connection failed.')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Cohorts' })).not.toBeInTheDocument();
  });

  it('shows an empty message when there are no cohorts', () => {
    render({ data: [], error: null });

    expect(screen.getByText('No Cohorts found.')).toBeInTheDocument();
  });

  describe('list view', () => {
    it('is the default view', () => {
      render({ data: cohorts, error: null });

      expect(screen.getByRole('table')).toBeInTheDocument();
      expect(screen.getByRole('button', { name: /List/ })).toHaveAttribute('aria-pressed', 'true');
    });

    it('sorts cohorts by name', () => {
      render({ data: cohorts, error: null });

      const names = screen
        .getAllByRole('link')
        .map((link) => link.getAttribute('href'))
        .filter((href) => href.startsWith('/cohort/'));
      expect(names[0]).toBe('/cohort/alone');
    });

    it('spans the cohort cells over its cluster queues and shows the parent', () => {
      render({ data: cohorts, error: null });

      const cohortCell = screen.getByRole('link', { name: 'child' }).closest('td');
      expect(cohortCell).toHaveAttribute('rowspan', '2');
      const row = cohortCell.closest('tr');
      expect(within(row).getByRole('link', { name: 'root' })).toHaveAttribute('href', '/cohort/root');
      expect(within(row).getByText('2')).toBeInTheDocument();
      expect(within(row).getByRole('link', { name: 'cq-1' })).toHaveAttribute('href', '/cluster-queue/cq-1');
      // The second queue of the cohort shares the spanned cells.
      expect(screen.getByRole('link', { name: 'cq-2' }).closest('tr').querySelectorAll('td')).toHaveLength(1);
      expect(screen.getAllByText('yaml:child')).toHaveLength(1);
    });

    it('shows a dash for a cohort without a parent', () => {
      render({ data: cohorts, error: null });

      const row = screen.getByRole('link', { name: 'alone' }).closest('tr');
      expect(within(row).getByText('-')).toBeInTheDocument();
    });

    it('explains a cohort without cluster queues', () => {
      render({ data: cohorts, error: null });

      // "root" is also the parent link of "child", so find the row by its message.
      const row = screen.getByText('No cluster queues found for this cohort.').closest('tr');
      expect(within(row).getByRole('link', { name: 'root' })).toHaveAttribute('href', '/cohort/root');
      expect(within(row).getByText('0')).toBeInTheDocument();
      expect(within(row).getByText('yaml:root')).toBeInTheDocument();
    });
  });

  describe('tree view', () => {
    const openTree = () => {
      render({ data: cohorts, error: null });
      fireEvent.click(screen.getByRole('button', { name: /Tree/ }));
    };

    it('switches to the tree and back', () => {
      openTree();

      expect(screen.queryByRole('table')).not.toBeInTheDocument();
      expect(screen.getByRole('button', { name: /Tree/ })).toHaveAttribute('aria-pressed', 'true');

      fireEvent.click(screen.getByRole('button', { name: /List/ }));
      expect(screen.getByRole('table')).toBeInTheDocument();
    });

    it('stays on the selected view when it is clicked again', () => {
      openTree();

      fireEvent.click(screen.getByRole('button', { name: /Tree/ }));

      expect(screen.getByRole('button', { name: /Tree/ })).toHaveAttribute('aria-pressed', 'true');
    });

    it('nests child cohorts below their parent with a child count', () => {
      openTree();

      const rootItem = screen.getByRole('link', { name: /^root/ });
      expect(rootItem).toHaveTextContent('1 child');
      expect(rootItem).toHaveTextContent('Cluster Queues: None');
      const childItem = screen.getByRole('link', { name: /^child/ });
      expect(childItem).toHaveTextContent('Cluster Queues: cq-1, cq-2');
      expect(childItem).toHaveAttribute('href', '/cohort/child');
    });

    it('collapses and expands the children of a cohort', async () => {
      openTree();
      // The only expand button belongs to the only cohort with children.
      const toggle = within(screen.getByRole('link', { name: /^root/ }).closest('li')).getAllByRole('button')[0];

      fireEvent.click(toggle);
      // The collapse unmounts its content once the exit transition has ended.
      await waitFor(() => expect(screen.queryByRole('link', { name: /^child/ })).not.toBeInTheDocument());

      fireEvent.click(toggle);
      expect(screen.getByRole('link', { name: /^child/ })).toBeInTheDocument();
    });

    it('shows each cohort in a parent cycle exactly once', () => {
      render({
        data: [
          { name: 'a', parentName: 'b', clusterQueues: [] },
          { name: 'b', parentName: 'a', clusterQueues: [] },
        ],
        error: null,
      });
      fireEvent.click(screen.getByRole('button', { name: /Tree/ }));

      expect(screen.getAllByRole('link', { name: /^a/ })).toHaveLength(1);
      expect(screen.getAllByRole('link', { name: /^b/ })).toHaveLength(1);
    });
  });
});

// names returns the tree as nested [name, children] pairs.
const names = (nodes) => nodes.map((node) => [node.name, names(node.children)]);

describe('buildCohortTree', () => {
  it('nests cohorts under their parent', () => {
    const tree = buildCohortTree([
      { name: 'org' },
      { name: 'team-a', parentName: 'org' },
      { name: 'team-b', parentName: 'org' },
    ]);
    expect(names(tree)).toEqual([['org', [['team-a', []], ['team-b', []]]]]);
  });

  it('keeps a cohort whose parent is not listed as a root', () => {
    expect(names(buildCohortTree([{ name: 'team', parentName: 'missing' }]))).toEqual([['team', []]]);
  });

  it('keeps children of a cycle under their parent', () => {
    const tree = buildCohortTree([
      { name: 'alpha-child', parentName: 'cycle-a' },
      { name: 'cycle-a', parentName: 'cycle-b' },
      { name: 'cycle-b', parentName: 'cycle-a' },
      { name: 'zeta-child', parentName: 'cycle-a' },
    ]);
    expect(names(tree)).toEqual([
      ['cycle-a', [['alpha-child', []], ['cycle-b', []], ['zeta-child', []]]],
    ]);
  });

  it('keeps cohorts that form a parent cycle', () => {
    const tree = buildCohortTree([
      { name: 'a', parentName: 'b' },
      { name: 'b', parentName: 'a' },
      { name: 'c', parentName: 'a' },
    ]);
    expect(names(tree)).toEqual([['a', [['b', []], ['c', []]]]]);
  });
});
