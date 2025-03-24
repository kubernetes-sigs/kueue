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
import { useParams } from 'react-router-dom';
import { Typography, Paper, Grid, CircularProgress, Table, TableBody, TableCell, TableContainer, TableHead, TableRow } from '@mui/material';
import useWebSocket from './useWebSocket';
import FlavorTable from './FlavorTable';
import './App.css';
import { Link } from 'react-router-dom';

const LocalQueueDetail = () => {
  const { namespace, queueName } = useParams();
  const queueUrl = `/ws/local-queue/${namespace}/${queueName}`;
  const workloadsUrl = `/ws/local-queue/${namespace}/${queueName}/workloads`;

  const { data: queueData, error: queueError } = useWebSocket(queueUrl);
  const { data: workloadsData, error: workloadsError } = useWebSocket(workloadsUrl);

  const [queue, setQueue] = useState(null);
  const [workloads, setWorkloads] = useState([]);

  useEffect(() => {
    if (queueData) setQueue(queueData);
  }, [queueData]);

  useEffect(() => {
    if (workloadsData) setWorkloads(workloadsData);
  }, [workloadsData]);

  if (!queue) return <CircularProgress />;
  if (queueError) return <Typography color="error">{queueError}</Typography>;
  if (workloadsError) return <Typography color="error">{workloadsError}</Typography>;

  return (
    <Paper style={{ padding: '16px', marginTop: '20px' }}>
      <Typography variant="h4" gutterBottom>Local Queue Detail: {queueName}</Typography>

      <Grid container spacing={2}>
        <Grid item xs={12} sm={6}>
          <Typography variant="body1"><strong>Name:</strong> {queue.metadata?.name}</Typography>
          <Typography variant="body1"><strong>Namespace:</strong> {queue.metadata?.namespace}</Typography>
          <Typography variant="body1"><strong>UID:</strong> {queue.metadata?.uid}</Typography>
          <Typography variant="body1"><strong>Creation Timestamp:</strong> {new Date(queue.metadata?.creationTimestamp).toLocaleString()}</Typography>
        </Grid>
        <Grid item xs={12} sm={6}>
          <Typography variant="body1"><strong>Cluster Queue:</strong><Link to={`/cluster-queue/${queue.spec?.clusterQueue}`}>{queue.spec?.clusterQueue}</Link></Typography>
          <Typography variant="body1"><strong>Admitted Workloads:</strong> {queue.status?.admittedWorkloads}</Typography>
          <Typography variant="body1"><strong>Pending Workloads:</strong> {queue.status?.pendingWorkloads}</Typography>
          <Typography variant="body1"><strong>Reserving Workloads:</strong> {queue.status?.reservingWorkloads}</Typography>
        </Grid>
      </Grid>

      <Typography variant="h5" gutterBottom style={{ marginTop: '20px' }}>Conditions</Typography>
      <TableContainer component={Paper}>
        <Table>
          <TableHead>
            <TableRow>
              <TableCell>Type</TableCell>
              <TableCell>Status</TableCell>
              <TableCell>Reason</TableCell>
              <TableCell>Last Transition Time</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {queue.status?.conditions?.map((condition, index) => (
              <TableRow key={index}>
                <TableCell>{condition.type}</TableCell>
                <TableCell>{condition.status}</TableCell>
                <TableCell>{condition.reason}</TableCell>
                <TableCell>{new Date(condition.lastTransitionTime).toLocaleString()}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableContainer>

      {/* Reusable FlavorTable Component for Flavor Usage */}
      <FlavorTable title="Flavor Usage" flavorData={queue.status?.flavorUsage} linkToFlavor={true} />

      {/* Reusable FlavorTable Component for Flavors Reservation */}
      <FlavorTable title="Flavors Reservation" flavorData={queue.status?.flavorsReservation} linkToFlavor={true} />

      <Typography variant="h5" gutterBottom style={{ marginTop: '20px' }}>Admitted Workloads</Typography>
      <TableContainer component={Paper}>
        <Table>
          <TableHead>
            <TableRow>
              <TableCell>Name</TableCell>
              <TableCell>Status</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {workloads.map((workload) => (
              <TableRow key={workload.metadata.name}>
                <TableCell><Link to={`/workload/${namespace}/${workload.metadata.name}`}>{workload.metadata.name}</Link></TableCell>
                <TableCell>{workload.status?.state || "Unknown"}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableContainer>
    </Paper>
  );
};

export default LocalQueueDetail;
