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
import { useParams,Link } from 'react-router-dom';
import { Typography, Paper, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, CircularProgress } from '@mui/material';
import useWebSocket from './useWebSocket';
import './App.css';

const CohortDetail = () => {
  const { cohortName } = useParams();
  const url = `/ws/cohort/${cohortName}`;
  const { data: cohortData, error } = useWebSocket(url);

  const [cohortDetails, setCohortDetails] = useState(null);

  useEffect(() => {
    if (cohortData) {
      setCohortDetails(cohortData);
    }
  }, [cohortData]);

  if (error) return <Typography color="error">{error}</Typography>;

  if (!cohortDetails) {
    return (
      <Paper style={{ padding: '16px', marginTop: '20px' }}>
        <Typography variant="h6">Loading...</Typography>
        <CircularProgress />
      </Paper>
    );
  }

  return (
    <Paper style={{ padding: '16px', marginTop: '20px' }}>
      <Typography variant="h4" gutterBottom>Cohort Detail: {cohortName}</Typography>
      <Typography variant="body1"><strong>Number of Cluster Queues:</strong> {cohortDetails.clusterQueues.length}</Typography>

      <Typography variant="h5" gutterBottom style={{ marginTop: '20px' }}>
        Cluster Queues in Cohort
      </Typography>
      {cohortDetails.clusterQueues.length === 0 ? (
        <Typography>No cluster queues are part of this cohort.</Typography>
      ) : (
        <TableContainer component={Paper}>
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
