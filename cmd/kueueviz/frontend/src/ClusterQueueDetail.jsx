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

import { CircularProgress, Grid, Paper, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, Tooltip, Typography } from '@mui/material';
import React, { useEffect, useState } from 'react';
import { useParams, Link } from 'react-router-dom'; 
import useWebSocket from './useWebSocket';
import './App.css';
import FlavorTable from './FlavorTable';
import ErrorMessage from './ErrorMessage';

const ClusterQueueDetail = () => {
  const { clusterQueueName } = useParams();
  const url = `/ws/cluster-queue/${clusterQueueName}`;
  const { data: clusterQueueData, error } = useWebSocket(url);

  const [clusterQueue, setClusterQueue] = useState(null);

  useEffect(() => {
    if (clusterQueueData) setClusterQueue(clusterQueueData);
  }, [clusterQueueData]);

  if (error) return <ErrorMessage error={error} />;

  if (!clusterQueue) {
    return (
      <Paper className="parentContainer">
        <Typography variant="h6">Loading...</Typography>
        <CircularProgress />
      </Paper>
    );
  }

  return (
    <Paper className="parentContainer">
      <Typography variant="h4" gutterBottom>Cluster Queue Detail: {clusterQueueName}</Typography>

      <Grid container spacing={2}>
        <Grid item xs={12} sm={6}>
          <Typography variant="body1"><strong>Name:</strong> {clusterQueue.metadata?.name}</Typography>
          <Typography variant="body1"><strong>UID:</strong> {clusterQueue.metadata?.uid}</Typography>
          <Typography variant="body1"><strong>Creation Timestamp:</strong> {new Date(clusterQueue.metadata?.creationTimestamp).toLocaleString()}</Typography>
          <Typography variant="body1"><strong>Cohort:</strong>
            <Link to={`/cohort/${clusterQueue.spec?.cohort}`}>{clusterQueue.spec?.cohort}</Link>
          </Typography>        </Grid>
        <Grid item xs={12} sm={6}>
          <Typography variant="body1"><strong>Admitted Workloads:</strong> {clusterQueue.status?.admittedWorkloads ?? 'N/A'}</Typography>
          <Typography variant="body1"><strong>Reserving Workloads:</strong> {clusterQueue.status?.reservingWorkloads ?? 'N/A'}</Typography>
          <Typography variant="body1"><strong>Pending Workloads:</strong> {clusterQueue.status?.pendingWorkloads ?? 'N/A'}</Typography>
        </Grid>
      </Grid>

      {/* Flavor Fungibility Section */}
      <Typography variant="h5" gutterBottom style={{ marginTop: '20px' }}>
        Flavor Fungibility
      </Typography>
      <Grid container spacing={2}>
        <Grid item xs={12} sm={6}>
          <Tooltip title="Determines whether to try the next flavor if a workload can borrow in the current one. Possible values: Borrow, TryNextFlavor.">
            <Typography variant="body1"><strong>When Can Borrow:</strong> {clusterQueue.spec?.flavorFungibility?.whenCanBorrow || 'Default (Borrow)'}</Typography>
          </Tooltip>
        </Grid>
        <Grid item xs={12} sm={6}>
          <Tooltip title="Determines whether to try the next flavor if preemption fails in the current one. Possible values: Preempt, TryNextFlavor.">
            <Typography variant="body1"><strong>When Can Preempt:</strong> {clusterQueue.spec?.flavorFungibility?.whenCanPreempt || 'Default (TryNextFlavor)'}</Typography>
          </Tooltip>
        </Grid>
      </Grid>
      {/* Preemption Section */}
      <Typography variant="h5" gutterBottom style={{ marginTop: '20px' }}>
        Preemption Settings
      </Typography>
      <Grid container spacing={2}>
        <Grid item xs={12} sm={4}>
          <Tooltip title="Determines whether a workload can preempt workloads in the cohort using more than their quota. Possible values: Never, LowerPriority, Any.">
            <Typography variant="body1"><strong>Reclaim Within Cohort:</strong> {clusterQueue.spec?.preemption?.reclaimWithinCohort || 'Not configured'}</Typography>
          </Tooltip>
        </Grid>
        <Grid item xs={12} sm={4}>
          <Tooltip title="Policy for borrowing within cohort. Possible values: Never, LowerPriority. Only active if reclaimWithinCohort is set.">
            <Typography variant="body1"><strong>Borrow Within Cohort:</strong> {clusterQueue.spec?.preemption?.borrowWithinCohort?.policy || 'Not configured'}</Typography>
          </Tooltip>
        </Grid>
        <Grid item xs={12} sm={4}>
          <Tooltip title="Determines if a workload can preempt others within the same ClusterQueue. Possible values: Never, LowerPriority, LowerOrNewerEqualPriority.">
            <Typography variant="body1"><strong>Within ClusterQueue:</strong> {clusterQueue.spec?.preemption?.withinClusterQueue || 'Not configured'}</Typography>
          </Tooltip>
        </Grid>
      </Grid>

      {/* Resource Groups Section */}
      <Typography variant="h5" gutterBottom style={{ marginTop: '20px' }}>
        Resource Groups (Quotas)
      </Typography>

      {clusterQueue.spec?.resourceGroups && clusterQueue.spec.resourceGroups.length > 0 ? (
        <TableContainer component={Paper} className="tableContainerWithBorder">
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Flavor</TableCell>
                <TableCell>Resource</TableCell>
                <TableCell>Nominal Quota</TableCell>
                <TableCell>Borrowing Limit</TableCell>
                <TableCell>Lending Limit</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {clusterQueue.spec.resourceGroups.map((group, groupIndex) => (
                <React.Fragment key={`resourceGroup-${groupIndex}`}>
                  {group.flavors.map((flavor, flavorIndex) => (
                    <React.Fragment key={`${groupIndex}-${flavor.name}`}>
                      {flavor.resources.map((resource, resourceIndex) => (
                        <TableRow key={`${groupIndex}-${flavor.name}-${resource.name}`}>
                          {/* Display Flavor Name with rowSpan across its resources */}
                          {resourceIndex === 0 && (
                            <TableCell rowSpan={flavor.resources.length}>
                              <Link to={`/resource-flavor/${flavor.name}`}>{flavor.name}</Link>
                            </TableCell>
                          )}

                          {/* Display Resource Name and Nominal Quota */}
                          <TableCell>{resource.name}</TableCell>
                          <TableCell>{resource.nominalQuota}</TableCell>
                          <TableCell>{resource.borrowingLimit}</TableCell>
                          <TableCell>{resource.lendingLimit}</TableCell>
                        </TableRow>
                      ))}
                    </React.Fragment>
                  ))}
                </React.Fragment>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      ) : (
        <Typography>No resource groups defined for this cluster queue.</Typography>
      )}

      {/* Flavor Reservation Table */}
      <FlavorTable title="Flavor Reservation" flavorData={clusterQueue.status?.flavorsReservation} linkToFlavor={true} 
                   showBorrowingColumn={true} />

      {/* Flavor Usage Table */}
      <FlavorTable title="Flavor Usage" flavorData={clusterQueue.status?.flavorsUsage} linkToFlavor={true} 
                   showBorrowingColumn={true} />

      {/* Local Queues Section */}
      <Typography variant="h5" gutterBottom style={{ marginTop: '20px' }}>
        Local Queues Using This Cluster Queue
      </Typography>
      <TableContainer component={Paper} className="tableContainerWithBorder">
        <Table>
          <TableHead>
            <TableRow>
              <TableCell>Queue Name</TableCell>
              <TableCell>Flavor</TableCell>
              <TableCell>Resource</TableCell>
              <TableCell>Reservation</TableCell>
              <TableCell>Usage</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {clusterQueue.queues && clusterQueue.queues.length > 0 ? (
              clusterQueue.queues.map((queue) => (
                <React.Fragment key={queue.name}>
                  {queue.reservation?.map((reservation, resIndex) => (
                    <React.Fragment key={`${queue.name}-reservation-${resIndex}`}>
                      {reservation.resources?.map((resource, resResourceIndex) => (
                        <TableRow key={`${queue.name}-${reservation.name}-${resource.name}`}>
                          {/* Display Queue Name with rowSpan across all flavors and resources */}
                          {resIndex === 0 && resResourceIndex === 0 && (
                            <TableCell rowSpan={queue.reservation.reduce((acc, flavor) => acc + (flavor.resources?.length || 0), 0)}>
                              <Link to={`/local-queue/${queue.namespace}/${queue.name}`}>{queue.name}</Link>
                            </TableCell>
                          )}

                          {/* Display Flavor Name with rowSpan across its resources */}
                          {resResourceIndex === 0 && (
                            <TableCell rowSpan={reservation.resources?.length || 1}>
                              <Link to={`/resource-flavor/${reservation.name}`}>{reservation.name}</Link>
                            </TableCell>
                          )}

                          {/* Display Resource Name, Reservation, and Usage */}
                          <TableCell>{resource.name}</TableCell>
                          <TableCell>{resource.total}</TableCell>
                          <TableCell>
                            {queue.usage?.find((usage) => usage.name === reservation.name)
                              ?.resources?.find((res) => res.name === resource.name)?.total || 'N/A'}
                          </TableCell>
                        </TableRow>
                      ))}
                    </React.Fragment>
                  ))}
                </React.Fragment>
              ))
            ) : (
              // Render an empty row to maintain table structure
              <TableRow>
                <TableCell colSpan={5}>No local queues using this cluster queue</TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </TableContainer>
    </Paper>
  );
};

export default ClusterQueueDetail;
