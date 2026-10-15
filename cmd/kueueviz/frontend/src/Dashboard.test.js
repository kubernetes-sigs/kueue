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

import { PREEMPTOR_UID_PATTERN } from './Dashboard';

describe('PREEMPTOR_UID_PATTERN', () => {
  it('extracts the preemptor workload UID from a Kueue eviction message', () => {
    const message =
      'Evicted: Preempted to accommodate a workload (UID: 1a2b-3c4d, JobUID: 5e6f-7a8b) due to prioritization in the ClusterQueue; preemptor path: /cq; preemptee path: /cq';

    const parts = message.split(PREEMPTOR_UID_PATTERN);

    expect(parts).toEqual([
      'Evicted: Preempted to accommodate a workload ',
      '1a2b-3c4d',
      ', JobUID: 5e6f-7a8b) due to prioritization in the ClusterQueue; preemptor path: /cq; preemptee path: /cq',
    ]);
  });

  it('does not match the JobUID', () => {
    expect('(JobUID: 5e6f-7a8b)'.split(PREEMPTOR_UID_PATTERN)).toEqual(['(JobUID: 5e6f-7a8b)']);
  });
});
