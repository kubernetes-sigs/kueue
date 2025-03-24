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
import { Typography, Paper, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, CircularProgress } from '@mui/material';
import useWebSocket from './useWebSocket';
import './App.css';

const Cohorts = () => {
  const { data: cohorts, error } = useWebSocket('/ws/cohorts');
  const [cohortList, setCohortList] = useState([]);

  useEffect(() => {
    if (cohorts && Array.isArray(cohorts)) {
      setCohortList(cohorts);
    }
  }, [cohorts]);

  if (error) return <Typography color="error">{error}</Typography>;

  return (
    <Paper style={{ padding: '16px', marginTop: '20px' }}>
      <Typography variant="h4" gutterBottom>Cohorts</Typography>
      {cohortList.length === 0 ? (
        <Typography>No Cohorts found.</Typography>
      ) : (
        <TableContainer component={Paper}>
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Cohort Name</TableCell>
                <TableCell>Number of Queues</TableCell>
                <TableCell>Cluster Queue Name</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {cohortList.map((cohort) => (
                cohort.clusterQueues && cohort.clusterQueues.length > 0 ? (
                  cohort.clusterQueues.map((queue, index) => (
                    <TableRow key={`${cohort.name}-${queue.name}`}>
                      {index === 0 && (
                        <TableCell rowSpan={cohort.clusterQueues.length}>
                          <Link to={`/cohort/${cohort.name}`}>{cohort.name}</Link>
                        </TableCell>
                      )}
                      {index === 0 && (
                        <TableCell rowSpan={cohort.clusterQueues.length}>
                          {cohort.clusterQueues.length}
                        </TableCell>
                      )}
                      <TableCell>
                        <Link to={`/cluster-queue/${queue.name}`}>{queue.name}</Link>
                      </TableCell>
                    </TableRow>
                  ))
                ) : (
                  <TableRow key={cohort.name}>
                    <TableCell>
                      <Link to={`/cohort/${cohort.name}`}>{cohort.name}</Link>
                    </TableCell>
                    <TableCell>{0}</TableCell>
                    <TableCell>No cluster queues found for this cohort.</TableCell>
                  </TableRow>
                )
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Paper>
  );
};

export default Cohorts;
