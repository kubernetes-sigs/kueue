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

import React from 'react';
import { Box, Tooltip, Typography } from '@mui/material';

/**
 * Coerce a resource value to a number.
 * The backend returns pre-parsed numeric values using Go's resource.Quantity
 * parser, so this is a simple numeric coercion for safety.
 */
export function toNumber(v) {
  if (v === undefined || v === null) return 0;
  const n = Number(v);
  return Number.isFinite(n) ? n : 0;
}

/**
 * Format a resource value for display. Keeps it human-readable.
 */
export function formatResourceValue(value, resourceName) {
  if (value === 0) return '0';
  if (resourceName === 'cpu') {
    if (value < 1) return `${Math.round(value * 1000)}m`;
    return `${parseFloat(value.toFixed(2))}`;
  }
  if (resourceName === 'memory') {
    const gi = value / (1024 * 1024 * 1024);
    if (gi >= 1) return `${parseFloat(gi.toFixed(1))}Gi`;
    const mi = value / (1024 * 1024);
    return `${parseFloat(mi.toFixed(0))}Mi`;
  }
  // GPUs or other integer resources
  return `${parseFloat(value.toFixed(2))}`;
}

/**
 * Aggregate usage/quota for a specific resource across all flavors of a ClusterQueue.
 *
 * @param {object} queue - A ClusterQueue object from the backend
 * @param {string} resourceName - e.g. "cpu", "memory", "nvidia.com/gpu"
 * @returns {{ usage: number, borrowed: number, quota: number, borrowingLimit: number|null }}
 *   borrowingLimit: finite number if explicitly set, null if unlimited (in cohort with no limit), 0 if not in a cohort
 */
export function aggregateResourceForQueue(queue, resourceName) {
  let quota = 0;
  let usage = 0;
  let borrowed = 0;
  let borrowingLimitTotal = 0;
  let hasExplicitBorrowingLimit = false;
  const inCohort = !!(queue.cohort || queue.spec?.cohortName);

  const resourceGroups = queue.resourceGroups || queue.spec?.resourceGroups || [];
  for (const rg of resourceGroups) {
    for (const flavor of (rg.flavors || [])) {
      for (const res of (flavor.resources || [])) {
        if (String(res.name) === resourceName) {
          quota += toNumber(res.nominalQuota);
          if (res.borrowingLimit != null) {
            hasExplicitBorrowingLimit = true;
            borrowingLimitTotal += toNumber(res.borrowingLimit);
          }
        }
      }
    }
  }

  const flavorsUsage = queue.flavorsUsage || queue.status?.flavorsUsage || [];
  for (const flavor of flavorsUsage) {
    for (const res of (flavor.resources || [])) {
      if (String(res.name) === resourceName) {
        usage += toNumber(res.total);
        borrowed += toNumber(res.borrowed);
      }
    }
  }

  // null means unlimited borrowing (in a cohort with no explicit limit).
  // 0 means no borrowing (not in a cohort).
  const borrowingLimit = hasExplicitBorrowingLimit ? borrowingLimitTotal : (inCohort ? null : 0);
  const unlimitedBorrowing = !hasExplicitBorrowingLimit && inCohort;
  return { usage, borrowed, quota, borrowingLimit, unlimitedBorrowing };
}

/**
 * Compute the effective quota ceiling from aggregation results.
 *
 * - Finite borrowingLimit: ceiling = quota + borrowingLimit
 * - Unlimited borrowing (null): ceiling = max(quota, usage) so that
 *   legitimate borrowed usage is never shown as overflow.
 * - No borrowing (0 or not in cohort): returns undefined (bar uses nominal quota).
 */
export function computeEffectiveQuota(r) {
  if (r.unlimitedBorrowing) {
    return r.usage > r.quota ? r.usage : undefined;
  }
  if (r.borrowingLimit != null && r.borrowingLimit > 0) return r.quota + r.borrowingLimit;
  return undefined;
}

/**
 * Discover all resource names from a list of ClusterQueues (or similar objects).
 * Returns sorted array with cpu and memory first.
 */
export function discoverResourceNames(queues) {
  const names = new Set();
  for (const queue of queues) {
    const resourceGroups = queue.resourceGroups || queue.spec?.resourceGroups || [];
    for (const rg of resourceGroups) {
      for (const flavor of (rg.flavors || [])) {
        for (const res of (flavor.resources || [])) {
          names.add(String(res.name));
        }
      }
    }
    const flavorsUsage = queue.flavorsUsage || queue.status?.flavorsUsage || [];
    for (const flavor of flavorsUsage) {
      for (const res of (flavor.resources || [])) {
        names.add(String(res.name));
      }
    }
  }
  const priority = ['cpu', 'memory'];
  return [...names].sort((a, b) => {
    const ai = priority.indexOf(a);
    const bi = priority.indexOf(b);
    if (ai !== -1 && bi !== -1) return ai - bi;
    if (ai !== -1) return -1;
    if (bi !== -1) return 1;
    return a.localeCompare(b);
  });
}

/**
 * UsageBar — A horizontal stacked bar showing resource utilization.
 *
 * Props:
 *   usage          - numeric, total usage (includes borrowed)
 *   borrowed       - numeric, how much of usage is borrowed
 *   quota          - numeric, nominal quota
 *   effectiveQuota - numeric (optional), nominalQuota + borrowingLimit; bar scales to this ceiling
 *   unlimitedBorrowing - boolean, if true the queue can borrow without limit
 *   label          - string, resource label (e.g. "cpu", "memory")
 *   compact        - boolean, if true renders a smaller version for table cells
 *   showText       - boolean, if true shows usage/quota text (default true)
 */
const UsageBar = ({ usage = 0, borrowed = 0, quota = 0, effectiveQuota, unlimitedBorrowing = false, label = '', compact = false, showText = true }) => {
  const ceiling = (effectiveQuota != null && effectiveQuota > quota) ? effectiveQuota : quota;
  const showQuotaMarker = effectiveQuota != null && effectiveQuota > quota && ceiling > 0;
  const quotaMarkerPercent = ceiling > 0 ? Math.min((quota / ceiling) * 100, 100) : 0;

  const ownUsage = Math.max(0, usage - borrowed);
  const percent = ceiling > 0 ? Math.min((usage / ceiling) * 100, 100) : (usage > 0 ? 100 : 0);
  const ownPercent = ceiling > 0 ? Math.min((ownUsage / ceiling) * 100, 100) : (ownUsage > 0 ? 100 : 0);
  const borrowedPercent = Math.max(0, percent - ownPercent);
  const isOverflow = usage > ceiling && ceiling > 0;
  const nominalPercent = quota > 0 ? Math.round((usage / quota) * 100) : 0;

  const height = compact ? 14 : 20;
  const width = compact ? 100 : '100%';
  const fontSize = compact ? '0.7rem' : '0.8rem';

  const tooltipContent = (
    <Box sx={{ p: 0.5 }}>
      <Typography variant="caption" display="block">
        <strong>{label || 'Resource'}</strong>
      </Typography>
      <Typography variant="caption" display="block">
        Usage: {formatResourceValue(usage, label)} / {formatResourceValue(quota, label)}
        {quota > 0 ? ` (${nominalPercent}% of nominal)` : ''}
      </Typography>
      {unlimitedBorrowing ? (
        <Typography variant="caption" display="block">
          Borrowing limit: unlimited
        </Typography>
      ) : showQuotaMarker ? (
        <Typography variant="caption" display="block">
          Effective limit: {formatResourceValue(effectiveQuota, label)} (nominal + borrowing limit)
        </Typography>
      ) : null}
      {borrowed > 0 && (
        <Typography variant="caption" display="block">
          Borrowed: {formatResourceValue(borrowed, label)}
        </Typography>
      )}
      {isOverflow && (
        <Typography variant="caption" display="block" color="error">
          Over {showQuotaMarker ? 'effective limit' : 'quota'} by {formatResourceValue(usage - ceiling, label)}
        </Typography>
      )}
    </Box>
  );

  return (
    <Box data-testid="usage-bar" sx={{ display: 'flex', alignItems: 'center', gap: 1, minWidth: compact ? 140 : 180 }}>
      <Tooltip title={tooltipContent} arrow placement="top">
        <Box sx={{
          width,
          minWidth: compact ? 80 : 100,
          height,
          bgcolor: '#e0e0e0',
          borderRadius: 1,
          overflow: 'hidden',
          display: 'flex',
          position: 'relative',
          border: isOverflow ? '2px solid #d32f2f' : '1px solid #ccc',
        }}>
          {/* Own usage segment */}
          <Box sx={{
            width: `${ownPercent}%`,
            height: '100%',
            bgcolor: '#1976d2',
            transition: 'width 0.3s ease',
          }} />
          {/* Borrowed segment */}
          {borrowedPercent > 0 && (
            <Box sx={{
              width: `${borrowedPercent}%`,
              height: '100%',
              bgcolor: '#ed6c02',
              transition: 'width 0.3s ease',
            }} />
          )}
          {/* Nominal quota marker when bar extends to effective quota */}
          {showQuotaMarker && (
            <Box sx={{
              position: 'absolute',
              left: `${quotaMarkerPercent}%`,
              top: 0,
              bottom: 0,
              width: '2px',
              bgcolor: 'rgba(0, 0, 0, 0.5)',
              zIndex: 1,
            }} />
          )}
          {/* Overflow indicator */}
          {isOverflow && (
            <Box sx={{
              position: 'absolute',
              right: 0,
              top: 0,
              bottom: 0,
              width: '20%',
              bgcolor: 'rgba(211, 47, 47, 0.3)',
              borderLeft: '2px dashed #d32f2f',
            }} />
          )}
        </Box>
      </Tooltip>
      {showText && (
        <Typography variant="caption" sx={{ fontSize, whiteSpace: 'nowrap', color: isOverflow ? '#d32f2f' : 'inherit' }}>
          {quota > 0 ? `${nominalPercent}%` : (usage > 0 ? 'No quota' : '-')}
        </Typography>
      )}
    </Box>
  );
};

export default UsageBar;
