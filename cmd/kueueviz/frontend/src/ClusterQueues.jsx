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
import { Typography, Paper, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, CircularProgress, Box } from '@mui/material';
import './App.css';
import ErrorMessage from './ErrorMessage';
import ViewYamlButton from './ViewYamlButton';

const ClusterQueues = () => {
  const { data: clusterQueues, error } = useWebSocket('/ws/cluster-queues');
  const [queues, setQueues] = useState([]);

  useEffect(() => {
    if (clusterQueues && Array.isArray(clusterQueues)) {
      setQueues(clusterQueues);
    }
  }, [clusterQueues]);

  if (error) return <ErrorMessage error={error} />;

  return (
    <Paper className="parentContainer">
      <Typography variant="h4" gutterBottom>Cluster Queues</Typography>
      {queues.length === 0 ? (
        <Typography>No Cluster Queues found.</Typography>
      ) : (
        <TableContainer component={Paper} className="tableContainerWithBorder">
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Name</TableCell>
                <TableCell>Cohort</TableCell>
                <TableCell>Flavors</TableCell>
                <TableCell>Admitted Workloads</TableCell>
                <TableCell>Pending Workloads</TableCell>
                <TableCell>Reserving Workloads</TableCell>
                <TableCell align="right">Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {queues.map((queue) => (
                <TableRow key={queue.name}>
                  <TableCell>
                    <Link to={`/cluster-queue/${queue.name}`}>{queue.name}</Link>
                  </TableCell>
                  <TableCell>
                    <Link to={`/cohort/${queue.cohort}`}>{queue.cohort || ''}</Link>
                  </TableCell>
                  <TableCell>
                    {queue.flavors ? (
                      queue.flavors.map((flavor, index) => (
                        <React.Fragment key={flavor}>
                          <Link to={`/resource-flavor/${flavor}`}>{flavor}</Link>
                          {index < queue.flavors.length - 1 && ', '}
                        </React.Fragment>
                      ))
                    ) : (
                      'N/A'
                    )}
                  </TableCell>
                  <TableCell>{queue.admittedWorkloads ?? 'N/A'}</TableCell>
                  <TableCell>{queue.pendingWorkloads ?? 'N/A'}</TableCell>
                  <TableCell>{queue.reservingWorkloads ?? 'N/A'}</TableCell>
                  <TableCell align="right">
                    <Box display="flex" justifyContent="flex-end">
                      <ViewYamlButton 
                        resourceType="clusterqueue"
                        resourceName={queue.name}
                      />
                    </Box>
                  </TableCell>
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
