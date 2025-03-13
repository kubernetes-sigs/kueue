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
import { Link } from 'react-router-dom';
import useWebSocket from './useWebSocket';
import { Typography, Paper, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, CircularProgress } from '@mui/material';
import './App.css';

const ClusterQueues = () => {
  const { data: clusterQueues, error } = useWebSocket('/ws/cluster-queues');
  const [queues, setQueues] = useState([]);

  useEffect(() => {
    if (clusterQueues && Array.isArray(clusterQueues)) {
      setQueues(clusterQueues);
    }
  }, [clusterQueues]);

  if (error) return <Typography color="error">{error}</Typography>;

  return (
    <Paper style={{ padding: '16px', marginTop: '20px' }}>
      <Typography variant="h4" gutterBottom>Cluster Queues</Typography>
      {queues.length === 0 ? (
        <Typography>No Cluster Queues found.</Typography>
      ) : (
        <TableContainer component={Paper}>
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Name</TableCell>
                <TableCell>Cohort</TableCell>
                <TableCell>Flavors</TableCell>
                <TableCell>Admitted Workloads</TableCell>
                <TableCell>Pending Workloads</TableCell>
                <TableCell>Reserving Workloads</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {queues.map((queue) => (
                <TableRow key={queue.name}>
                  <TableCell><Link to={`/cluster-queue/${queue.name}`}>{queue.name}</Link></TableCell>
                  <TableCell><Link to={`/cohort/${queue.cohort}`}>{queue.cohort || ''}</Link></TableCell>
                  <TableCell>
                    {queue.flavors.map((flavor, index) => (
                      <React.Fragment key={flavor}>
                        <Link to={`/resource-flavor/${flavor}`}>{flavor}</Link>
                        {index < queue.flavors.length - 1 && ', '}
                      </React.Fragment>
                    ))}
                  </TableCell>
                  <TableCell>{queue.admittedWorkloads}</TableCell>
                  <TableCell>{queue.pendingWorkloads}</TableCell>
                  <TableCell>{queue.reservingWorkloads}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Paper>
  );
};

export default ClusterQueues;
