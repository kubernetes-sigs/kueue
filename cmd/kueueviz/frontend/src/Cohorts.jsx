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
import { Typography, Paper, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, CircularProgress, Box } from '@mui/material';
import useWebSocket from './useWebSocket';
import './App.css';
import ErrorMessage from './ErrorMessage';
import ViewYamlButton from './ViewYamlButton';

const Cohorts = () => {
  const { data: cohorts, error } = useWebSocket('/ws/cohorts');
  const [cohortList, setCohortList] = useState([]);

  useEffect(() => {
    if (cohorts && Array.isArray(cohorts)) {
      setCohortList(cohorts);
    }
  }, [cohorts]);

  if (error) return <ErrorMessage error={error} />;

  return (
    <Paper className="parentContainer">
      <Typography variant="h4" gutterBottom>Cohorts</Typography>
      {cohortList.length === 0 ? (
        <Typography>No Cohorts found.</Typography>
      ) : (
        <TableContainer component={Paper} className="tableContainerWithBorder">
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Cohort Name</TableCell>
                <TableCell>Number of Queues</TableCell>
                <TableCell>Cluster Queue Name</TableCell>
                <TableCell align="right">Actions</TableCell>
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
                      {index === 0 && (
                        <TableCell rowSpan={cohort.clusterQueues.length} align="right">
                          <Box display="flex" justifyContent="flex-end">
                            <ViewYamlButton 
                              resourceType="cohort"
                              resourceName={cohort.name}
                            />
                          </Box>
                        </TableCell>
                      )}
                    </TableRow>
                  ))
                ) : (
                  <TableRow key={cohort.name}>
                    <TableCell>
                      <Link to={`/cohort/${cohort.name}`}>{cohort.name}</Link>
                    </TableCell>
                    <TableCell>{0}</TableCell>
                    <TableCell>No cluster queues found for this cohort.</TableCell>
                    <TableCell align="right">
                      <Box display="flex" justifyContent="flex-end">
                        <ViewYamlButton 
                          resourceType="cohort"
                          resourceName={cohort.name}
                        />
                      </Box>
                    </TableCell>
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
