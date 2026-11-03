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
import { getWorkloadStatus } from './workloadStatus';

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
