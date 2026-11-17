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

import { describe, it, expect } from 'vitest';
import { getWorkloadStatus, podCount } from './workloadStatus';

const withConditions = (...conditions) => ({ status: { conditions } });
const condition = (type, status) => ({ type, status });

describe('getWorkloadStatus', () => {
  it.each([
    ['no status', {}, 'Pending'],
    ['no conditions', withConditions(), 'Pending'],
    [
      'quota not reserved yet',
      withConditions(condition('QuotaReserved', 'False')),
      'Pending',
    ],
    [
      'quota reserved',
      withConditions(condition('QuotaReserved', 'True')),
      'QuotaReserved',
    ],
    [
      'admitted',
      withConditions(condition('QuotaReserved', 'True'), condition('Admitted', 'True')),
      'Admitted',
    ],
    [
      'finished after admission',
      withConditions(
        condition('QuotaReserved', 'True'),
        condition('Admitted', 'True'),
        condition('Finished', 'True')
      ),
      'Finished',
    ],
    [
      'evicted and back to pending',
      withConditions(
        condition('QuotaReserved', 'False'),
        condition('Admitted', 'False'),
        condition('Evicted', 'True')
      ),
      'Pending',
    ],
  ])('%s', (_, workload, want) => {
    expect(getWorkloadStatus(workload)).toBe(want);
  });
});

describe('podCount', () => {
  it.each([
    ['no pod sets', { spec: {} }, undefined],
    ['one pod set', { spec: { podSets: [{ count: 3 }] } }, 3],
    ['several pod sets', { spec: { podSets: [{ count: 1 }, { count: 4 }] } }, 5],
  ])('%s', (_description, workload, want) => {
    expect(podCount(workload)).toBe(want);
  });
});
