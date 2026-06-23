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
import {
  toNumber,
  formatResourceValue,
  aggregateResourceForQueue,
  computeEffectiveQuota,
  discoverResourceNames,
} from './UsageBar';

// ---------------------------------------------------------------------------
// toNumber
// ---------------------------------------------------------------------------
describe('toNumber', () => {
  it('returns 0 for null and undefined', () => {
    expect(toNumber(null)).toBe(0);
    expect(toNumber(undefined)).toBe(0);
  });

  it('coerces numeric values', () => {
    expect(toNumber(42)).toBe(42);
    expect(toNumber(0.5)).toBe(0.5);
    expect(toNumber(0)).toBe(0);
  });

  it('coerces numeric strings', () => {
    expect(toNumber('3.5')).toBe(3.5);
    expect(toNumber('0')).toBe(0);
  });

  it('returns 0 for non-finite values', () => {
    expect(toNumber(NaN)).toBe(0);
    expect(toNumber(Infinity)).toBe(0);
    expect(toNumber(-Infinity)).toBe(0);
    expect(toNumber('not-a-number')).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// formatResourceValue
// ---------------------------------------------------------------------------
describe('formatResourceValue', () => {
  it('returns "0" for zero', () => {
    expect(formatResourceValue(0, 'cpu')).toBe('0');
    expect(formatResourceValue(0, 'memory')).toBe('0');
  });

  it('formats CPU millicores', () => {
    expect(formatResourceValue(0.5, 'cpu')).toBe('500m');
    expect(formatResourceValue(0.25, 'cpu')).toBe('250m');
    expect(formatResourceValue(0.001, 'cpu')).toBe('1m');
  });

  it('formats CPU whole cores', () => {
    expect(formatResourceValue(4, 'cpu')).toBe('4');
    expect(formatResourceValue(2.5, 'cpu')).toBe('2.5');
  });

  it('formats memory in Gi', () => {
    const gi = 1024 * 1024 * 1024;
    expect(formatResourceValue(2 * gi, 'memory')).toBe('2Gi');
    expect(formatResourceValue(1.5 * gi, 'memory')).toBe('1.5Gi');
  });

  it('formats memory in Mi when < 1Gi', () => {
    const mi = 1024 * 1024;
    expect(formatResourceValue(512 * mi, 'memory')).toBe('512Mi');
    expect(formatResourceValue(256 * mi, 'memory')).toBe('256Mi');
  });

  it('formats GPU / generic resources', () => {
    expect(formatResourceValue(2, 'nvidia.com/gpu')).toBe('2');
    expect(formatResourceValue(1, 'nvidia.com/gpu')).toBe('1');
  });
});

// ---------------------------------------------------------------------------
// aggregateResourceForQueue
// ---------------------------------------------------------------------------
describe('aggregateResourceForQueue', () => {
  const makeQueue = ({ cohort, resourceGroups, flavorsUsage }) => ({
    cohort: cohort ?? '',
    resourceGroups: resourceGroups ?? [],
    flavorsUsage: flavorsUsage ?? [],
  });

  it('standalone CQ (no cohort): borrowingLimit=0, unlimitedBorrowing=false', () => {
    const queue = makeQueue({
      cohort: '',
      resourceGroups: [{
        flavors: [{
          name: 'default',
          resources: [{ name: 'cpu', nominalQuota: 4 }],
        }],
      }],
      flavorsUsage: [{
        name: 'default',
        resources: [{ name: 'cpu', total: 2, borrowed: 0 }],
      }],
    });
    const r = aggregateResourceForQueue(queue, 'cpu');
    expect(r.quota).toBe(4);
    expect(r.usage).toBe(2);
    expect(r.borrowed).toBe(0);
    expect(r.borrowingLimit).toBe(0);
    expect(r.unlimitedBorrowing).toBe(false);
  });

  it('CQ in cohort with no borrowingLimit: unlimited borrowing', () => {
    const queue = makeQueue({
      cohort: 'team-alpha',
      resourceGroups: [{
        flavors: [{
          name: 'default',
          resources: [{ name: 'cpu', nominalQuota: 2 }],
        }],
      }],
      flavorsUsage: [{
        name: 'default',
        resources: [{ name: 'cpu', total: 3, borrowed: 1 }],
      }],
    });
    const r = aggregateResourceForQueue(queue, 'cpu');
    expect(r.quota).toBe(2);
    expect(r.usage).toBe(3);
    expect(r.borrowed).toBe(1);
    expect(r.borrowingLimit).toBeNull();
    expect(r.unlimitedBorrowing).toBe(true);
  });

  it('CQ in cohort with finite borrowingLimit', () => {
    const queue = makeQueue({
      cohort: 'team-beta',
      resourceGroups: [{
        flavors: [{
          name: 'default',
          resources: [{ name: 'cpu', nominalQuota: 4, borrowingLimit: 2 }],
        }],
      }],
      flavorsUsage: [{
        name: 'default',
        resources: [{ name: 'cpu', total: 5, borrowed: 1 }],
      }],
    });
    const r = aggregateResourceForQueue(queue, 'cpu');
    expect(r.quota).toBe(4);
    expect(r.usage).toBe(5);
    expect(r.borrowed).toBe(1);
    expect(r.borrowingLimit).toBe(2);
    expect(r.unlimitedBorrowing).toBe(false);
  });

  it('CQ in cohort with borrowingLimit=0: explicit no-borrow', () => {
    const queue = makeQueue({
      cohort: 'team-gamma',
      resourceGroups: [{
        flavors: [{
          name: 'default',
          resources: [{ name: 'cpu', nominalQuota: 4, borrowingLimit: 0 }],
        }],
      }],
      flavorsUsage: [{
        name: 'default',
        resources: [{ name: 'cpu', total: 3, borrowed: 0 }],
      }],
    });
    const r = aggregateResourceForQueue(queue, 'cpu');
    expect(r.quota).toBe(4);
    expect(r.borrowingLimit).toBe(0);
    expect(r.unlimitedBorrowing).toBe(false);
  });

  it('aggregates across multiple flavors', () => {
    const queue = makeQueue({
      cohort: 'team-beta',
      resourceGroups: [
        {
          flavors: [{
            name: 'flavor-a',
            resources: [{ name: 'cpu', nominalQuota: 2, borrowingLimit: 1 }],
          }],
        },
        {
          flavors: [{
            name: 'flavor-b',
            resources: [{ name: 'cpu', nominalQuota: 3, borrowingLimit: 2 }],
          }],
        },
      ],
      flavorsUsage: [
        { name: 'flavor-a', resources: [{ name: 'cpu', total: 2, borrowed: 0.5 }] },
        { name: 'flavor-b', resources: [{ name: 'cpu', total: 4, borrowed: 1 }] },
      ],
    });
    const r = aggregateResourceForQueue(queue, 'cpu');
    expect(r.quota).toBe(5);
    expect(r.usage).toBe(6);
    expect(r.borrowed).toBe(1.5);
    expect(r.borrowingLimit).toBe(3);
  });

  it('returns zeros for a resource not present in the queue', () => {
    const queue = makeQueue({
      cohort: '',
      resourceGroups: [{
        flavors: [{
          name: 'default',
          resources: [{ name: 'cpu', nominalQuota: 4 }],
        }],
      }],
    });
    const r = aggregateResourceForQueue(queue, 'nvidia.com/gpu');
    expect(r.quota).toBe(0);
    expect(r.usage).toBe(0);
    expect(r.borrowed).toBe(0);
  });

  it('works with spec-nested structure (e.g. from detail endpoint)', () => {
    const queue = {
      spec: {
        cohortName: 'team-alpha',
        resourceGroups: [{
          flavors: [{
            name: 'default',
            resources: [{ name: 'memory', nominalQuota: 4294967296 }],
          }],
        }],
      },
      status: {
        flavorsUsage: [{
          name: 'default',
          resources: [{ name: 'memory', total: 2147483648, borrowed: 0 }],
        }],
      },
    };
    const r = aggregateResourceForQueue(queue, 'memory');
    expect(r.quota).toBe(4294967296);
    expect(r.usage).toBe(2147483648);
    expect(r.unlimitedBorrowing).toBe(true);
  });

  it('mixed borrowingLimit across flavors: one set, one nil', () => {
    // If any flavor for a resource has an explicit borrowingLimit,
    // hasExplicitBorrowingLimit becomes true and the total is the sum
    // of the explicit values (nil flavors contribute 0).
    const queue = makeQueue({
      cohort: 'team-gamma',
      resourceGroups: [
        {
          flavors: [{
            name: 'flavor-a',
            resources: [{ name: 'cpu', nominalQuota: 2, borrowingLimit: 1 }],
          }],
        },
        {
          flavors: [{
            name: 'flavor-b',
            resources: [{ name: 'cpu', nominalQuota: 2 }], // no borrowingLimit
          }],
        },
      ],
    });
    const r = aggregateResourceForQueue(queue, 'cpu');
    expect(r.quota).toBe(4);
    // hasExplicitBorrowingLimit is true because flavor-a has it
    expect(r.borrowingLimit).toBe(1);
    expect(r.unlimitedBorrowing).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// computeEffectiveQuota
// ---------------------------------------------------------------------------
describe('computeEffectiveQuota', () => {
  it('unlimited borrowing, usage > quota: returns usage (bar stretches)', () => {
    const r = { usage: 6, quota: 4, borrowingLimit: null, unlimitedBorrowing: true };
    expect(computeEffectiveQuota(r)).toBe(6);
  });

  it('unlimited borrowing, usage <= quota: returns undefined (use nominal)', () => {
    const r = { usage: 3, quota: 4, borrowingLimit: null, unlimitedBorrowing: true };
    expect(computeEffectiveQuota(r)).toBeUndefined();
  });

  it('finite borrowingLimit: returns quota + borrowingLimit', () => {
    const r = { usage: 5, quota: 4, borrowingLimit: 2, unlimitedBorrowing: false };
    expect(computeEffectiveQuota(r)).toBe(6);
  });

  it('borrowingLimit=0 (explicit no-borrow in cohort): returns undefined', () => {
    const r = { usage: 3, quota: 4, borrowingLimit: 0, unlimitedBorrowing: false };
    expect(computeEffectiveQuota(r)).toBeUndefined();
  });

  it('standalone (not in cohort): returns undefined', () => {
    const r = { usage: 3, quota: 4, borrowingLimit: 0, unlimitedBorrowing: false };
    expect(computeEffectiveQuota(r)).toBeUndefined();
  });

  it('unlimited borrowing never shows overflow (regression for Bug 1)', () => {
    // A CQ in a cohort with no borrowingLimit and usage = 150% of quota
    // must NOT produce an effectiveQuota that causes isOverflow = true.
    const r = { usage: 6, quota: 4, borrowingLimit: null, unlimitedBorrowing: true };
    const effective = computeEffectiveQuota(r);
    // effective equals usage, so ceiling=usage, isOverflow = usage > ceiling = false
    expect(effective).toBe(6);
    const ceiling = (effective != null && effective > r.quota) ? effective : r.quota;
    expect(r.usage).toBeLessThanOrEqual(ceiling);
  });
});

// ---------------------------------------------------------------------------
// discoverResourceNames
// ---------------------------------------------------------------------------
describe('discoverResourceNames', () => {
  it('returns sorted names with cpu and memory first', () => {
    const queues = [{
      resourceGroups: [{
        flavors: [{
          name: 'default',
          resources: [
            { name: 'nvidia.com/gpu' },
            { name: 'memory' },
            { name: 'cpu' },
          ],
        }],
      }],
    }];
    expect(discoverResourceNames(queues)).toEqual(['cpu', 'memory', 'nvidia.com/gpu']);
  });

  it('deduplicates across queues and flavorsUsage', () => {
    const queues = [
      {
        resourceGroups: [{
          flavors: [{ name: 'f1', resources: [{ name: 'cpu' }] }],
        }],
        flavorsUsage: [{ name: 'f1', resources: [{ name: 'cpu' }, { name: 'memory' }] }],
      },
      {
        resourceGroups: [{
          flavors: [{ name: 'f1', resources: [{ name: 'cpu' }, { name: 'memory' }] }],
        }],
      },
    ];
    expect(discoverResourceNames(queues)).toEqual(['cpu', 'memory']);
  });

  it('returns empty array for no queues', () => {
    expect(discoverResourceNames([])).toEqual([]);
  });

  it('handles spec-nested structure', () => {
    const queues = [{
      spec: {
        resourceGroups: [{
          flavors: [{ name: 'f1', resources: [{ name: 'cpu' }] }],
        }],
      },
      status: {
        flavorsUsage: [{ name: 'f1', resources: [{ name: 'memory' }] }],
      },
    }];
    expect(discoverResourceNames(queues)).toEqual(['cpu', 'memory']);
  });
});
