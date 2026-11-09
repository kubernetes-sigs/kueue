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
import { formatTaint, formatToleration } from './taints';

describe('formatTaint', () => {
  it.each([
    ['key, value and effect', { key: 'gpu', value: 'true', effect: 'NoSchedule' }, 'gpu=true:NoSchedule'],
    ['no value', { key: 'node-role.kubernetes.io/control-plane', effect: 'NoSchedule' }, 'node-role.kubernetes.io/control-plane:NoSchedule'],
    ['empty value from the nodes list', { key: 'spot', value: '', effect: 'NoExecute' }, 'spot:NoExecute'],
  ])('%s', (_, taint, want) => {
    expect(formatTaint(taint)).toBe(want);
  });
});

describe('formatToleration', () => {
  it.each([
    ['Equal with value', { key: 'gpu', operator: 'Equal', value: 'true', effect: 'NoSchedule' }, 'gpu=true:NoSchedule'],
    ['operator omitted', { key: 'gpu', value: 'true' }, 'gpu=true'],
    ['Exists with key', { key: 'spot', operator: 'Exists', effect: 'NoSchedule' }, 'spot:NoSchedule op=Exists'],
    ['Exists without key tolerates everything', { operator: 'Exists' }, 'op=Exists'],
    ['tolerationSeconds', { key: 'node.kubernetes.io/not-ready', operator: 'Exists', effect: 'NoExecute', tolerationSeconds: 300 }, 'node.kubernetes.io/not-ready:NoExecute op=Exists for 300s'],
  ])('%s', (_, toleration, want) => {
    expect(formatToleration(toleration)).toBe(want);
  });
});
