/*
Copyright 2024 The Kubernetes Authors.

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

import React, { useState, useEffect } from 'react';
import { useParams, Link } from 'react-router-dom';
import { Typography, Paper, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, CircularProgress, Grid, Box } from '@mui/material';
import useWebSocket from './useWebSocket';
import './App.css';
import ErrorMessage from './ErrorMessage';
import UsageBar, { toNumber, formatResourceValue, computeEffectiveQuota } from './UsageBar';

const CohortDetail = () => {
  const { cohortName } = useParams();
  const url = `/ws/cohort/${cohortName}`;
  const { data: cohortData, error } = useWebSocket(url);

  const [cohortDetails, setCohortDetails] = useState(null);

  useEffect(() => {
    if (cohortData) {
      const sorted = { ...cohortData };
      if (sorted.clusterQueues) {
        sorted.clusterQueues = [...sorted.clusterQueues].sort((a, b) => (a.name || '').localeCompare(b.name || ''));
      }
      setCohortDetails(sorted);
    }
  }, [cohortData]);

  if (error) return <ErrorMessage error={error} />;

  if (!cohortDetails) {
    return (
      <Paper className="parentContainer">
        <Typography variant="h6">Loading...</Typography>
        <CircularProgress />
      </Paper>
    );
  }

  return (
    <Paper className="parentContainer">
      <Typography variant="h4" gutterBottom>Cohort Detail: {cohortName}</Typography>
      <Typography variant="body1"><strong>Number of Cluster Queues:</strong> {cohortDetails.clusterQueues.length}</Typography>

      {/* Cohort Resource Utilization */}
      {cohortDetails.clusterQueues.length > 0 && (() => {
        const resourceNames = new Set();
        cohortDetails.clusterQueues.forEach(cq => {
          (cq.spec?.resourceGroups || []).forEach(rg => {
            (rg.flavors || []).forEach(f => {
              (f.resources || []).forEach(r => resourceNames.add(String(r.name)));
            });
          });
        });

        const aggregated = {};
        resourceNames.forEach(resName => {
          let totalQuota = 0, totalUsage = 0;
          cohortDetails.clusterQueues.forEach(cq => {
            (cq.spec?.resourceGroups || []).forEach(rg => {
              (rg.flavors || []).forEach(f => {
                (f.resources || []).forEach(r => {
                  if (String(r.name) === resName) totalQuota += toNumber(r.nominalQuota);
                });
              });
            });
            (cq.status?.flavorsUsage || []).forEach(f => {
              (f.resources || []).forEach(r => {
                if (String(r.name) === resName) {
                  totalUsage += toNumber(r.total);
                }
              });
            });
          });
          aggregated[resName] = { quota: totalQuota, usage: totalUsage };
        });

        return (
          <>
            <Typography variant="h5" gutterBottom style={{ marginTop: '20px' }}>
              Cohort Resource Utilization
            </Typography>
            <Grid container spacing={2} sx={{ mb: 2, maxWidth: '95%' }}>
              {Object.entries(aggregated).map(([resName, data]) => (
                <Grid item xs={12} sm={4} key={resName}>
                  <Paper elevation={2} sx={{ p: 2 }}>
                    <Typography variant="subtitle2" gutterBottom>{resName}</Typography>
                    <UsageBar usage={data.usage} quota={data.quota} label={resName} />
                    <Typography variant="caption" display="block" sx={{ mt: 0.5 }}>
                      {formatResourceValue(data.usage, resName)} / {formatResourceValue(data.quota, resName)}
                    </Typography>
                  </Paper>
                </Grid>
              ))}
            </Grid>

            {/* Per-CQ Usage Breakdown */}
            {(() => {
              const hasFairSharing = cohortDetails.clusterQueues.some(cq => cq.status?.fairSharing?.weightedShare != null);
              return (
                <>
                  <Typography variant="h5" gutterBottom style={{ marginTop: '10px' }}>
                    Per-Queue Resource Usage
                  </Typography>
                  <TableContainer component={Paper} className="tableContainerWithBorder">
                    <Table size="small">
                      <TableHead>
                        <TableRow>
                          <TableCell>Queue</TableCell>
                          {hasFairSharing && <TableCell>Weighted Share</TableCell>}
                          {[...resourceNames].map(r => (
                            <TableCell key={r} sx={{ minWidth: 180 }}>{r}</TableCell>
                          ))}
                        </TableRow>
                      </TableHead>
                      <TableBody>
                        {cohortDetails.clusterQueues.map(cq => {
                          const perRes = {};
                          resourceNames.forEach(resName => {
                            let quota = 0, usage = 0, borrowed = 0;
                            let borrowingLimitTotal = 0, hasExplicitBorrowingLimit = false;
                            (cq.spec?.resourceGroups || []).forEach(rg => {
                              (rg.flavors || []).forEach(f => {
                                (f.resources || []).forEach(r => {
                                  if (String(r.name) === resName) {
                                    quota += toNumber(r.nominalQuota);
                                    if (r.borrowingLimit != null) {
                                      hasExplicitBorrowingLimit = true;
                                      borrowingLimitTotal += toNumber(r.borrowingLimit);
                                    }
                                  }
                                });
                              });
                            });
                            (cq.status?.flavorsUsage || []).forEach(f => {
                              (f.resources || []).forEach(r => {
                                if (String(r.name) === resName) {
                                  usage += toNumber(r.total);
                                  borrowed += toNumber(r.borrowed);
                                }
                              });
                            });
                            const borrowingLimit = hasExplicitBorrowingLimit ? borrowingLimitTotal : null;
                            const unlimitedBorrowing = !hasExplicitBorrowingLimit;
                            perRes[resName] = { quota, usage, borrowed, borrowingLimit, unlimitedBorrowing };
                          });
                          return (
                            <TableRow key={cq.name}>
                              <TableCell><Link to={`/cluster-queue/${cq.name}`}>{cq.name}</Link></TableCell>
                              {hasFairSharing && (
                                <TableCell>{cq.status?.fairSharing?.weightedShare ?? '-'}</TableCell>
                              )}
                              {[...resourceNames].map(resName => (
                                <TableCell key={resName}>
                                  <UsageBar usage={perRes[resName].usage} borrowed={perRes[resName].borrowed}
                                    quota={perRes[resName].quota} effectiveQuota={computeEffectiveQuota(perRes[resName])}
                                    unlimitedBorrowing={perRes[resName].unlimitedBorrowing} label={resName} compact />
                                </TableCell>
                              ))}
                            </TableRow>
                          );
                        })}
                      </TableBody>
                    </Table>
                  </TableContainer>
                </>
              );
            })()}
          </>
        );
      })()}

      <Typography variant="h5" gutterBottom style={{ marginTop: '20px' }}>
        Cluster Queues in Cohort
      </Typography>
      {cohortDetails.clusterQueues.length === 0 ? (
        <Typography>No cluster queues are part of this cohort.</Typography>
      ) : (
        <TableContainer component={Paper} className="tableContainerWithBorder">
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Queue Name</TableCell>
                <TableCell colSpan={2} align="center">Flavor Fungibility</TableCell>
                <TableCell colSpan={3} align="center">Preemption</TableCell>
                <TableCell>Queueing Strategy</TableCell>
              </TableRow>
              <TableRow>
                <TableCell></TableCell>
                <TableCell>When Can Borrow</TableCell>
                <TableCell>When Can Preempt</TableCell>
                <TableCell>Borrow Within Cohort</TableCell>
                <TableCell>Reclaim Within Cohort</TableCell>
                <TableCell>Within Cluster Queue</TableCell>
                <TableCell></TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {cohortDetails.clusterQueues.map((queue) => (
                <TableRow key={queue.name}>
                  <TableCell><Link to={`/cluster-queue/${queue.name}`}>{queue.name}</Link></TableCell>
                  <TableCell>{queue.spec.flavorFungibility?.whenCanBorrow || "N/A"}</TableCell>
                  <TableCell>{queue.spec.flavorFungibility?.whenCanPreempt || "N/A"}</TableCell>
                  <TableCell>{queue.spec.preemption?.borrowWithinCohort?.policy || "N/A"}</TableCell>
                  <TableCell>{queue.spec.preemption?.reclaimWithinCohort || "N/A"}</TableCell>
                  <TableCell>{queue.spec.preemption?.withinClusterQueue || "N/A"}</TableCell>
                  <TableCell>{queue.spec.queueingStrategy || "N/A"}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Paper>
  );
};

export default CohortDetail;
