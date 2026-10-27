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

import { describe, it, expect, vi } from 'vitest';

vi.mock('./useWebSocket', () => ({
  default: vi.fn(),
}));

// env.js reads window at load time, which the node environment lacks.
vi.mock('./env', () => ({ env: {} }));

import { buildCohortTree } from './Cohorts';

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

  it('keeps cohorts that form a parent cycle', () => {
    const tree = buildCohortTree([
      { name: 'a', parentName: 'b' },
      { name: 'b', parentName: 'a' },
      { name: 'c', parentName: 'a' },
    ]);
    expect(names(tree)).toEqual([['a', [['b', []], ['c', []]]]]);
  });
});
