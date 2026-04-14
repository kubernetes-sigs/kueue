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

import { CircularProgress, Paper, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, Typography, Box } from '@mui/material';
import React, { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import useWebSocket from './useWebSocket';
import './App.css';
import ErrorMessage from './ErrorMessage';
import ViewYamlButton from './ViewYamlButton';
import { toNumber, formatResourceValue, discoverResourceNames } from './UsageBar';

const LocalQueues = () => {
  const { data: localQueues, error } = useWebSocket('/ws/local-queues');
  const [queues, setQueues] = useState([]);

  useEffect(() => {
    if (localQueues && Array.isArray(localQueues)) {
      setQueues([...localQueues].sort((a, b) =>
        (a.namespace || '').localeCompare(b.namespace || '') || (a.name || '').localeCompare(b.name || '')
      ));
    }
  }, [localQueues]);

  if (error) return <ErrorMessage error={error} />;

  const resourceNames = discoverResourceNames(queues);

  // Group queues by namespace
  const queuesByNamespace = queues.reduce((acc, queue) => {
    const namespace = queue.namespace;
    if (!acc[namespace]) {
      acc[namespace] = [];
    }
    acc[namespace].push(queue);
    return acc;
  }, {});

  return (
    <Paper className="parentContainer">
      <Typography variant="h4" gutterBottom>Local Queues</Typography>
      {queues.length === 0 ? (
        <Typography>No Local Queues found.</Typography>
      ) : (
        <TableContainer component={Paper} className="tableContainerWithBorder">
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Namespace</TableCell>
                <TableCell>Name</TableCell>
                <TableCell>Cluster Queue</TableCell>
                <TableCell>Admitted Workloads</TableCell>
                <TableCell>Pending Workloads</TableCell>
                <TableCell>Reserving Workloads</TableCell>
                {resourceNames.map(r => (
                  <TableCell key={r}>{r}</TableCell>
                ))}
                <TableCell align="right">Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {Object.entries(queuesByNamespace).map(([namespace, queues]) => (
                queues.map((queue, index) => (
                  <TableRow key={queue.name}>
                    {index === 0 && (
                      <TableCell rowSpan={queues.length}>
                        {namespace}
                      </TableCell>
                    )}
                    <TableCell>
                      <Link to={`/local-queue/${queue.namespace}/${queue.name}`}>
                        {queue.name}
                      </Link>
                    </TableCell>
                    <TableCell>
                      <Link to={`/cluster-queue/${queue.spec?.clusterQueue}`}>{queue.spec?.clusterQueue}</Link>
                    </TableCell>
                    <TableCell>{queue.status?.admittedWorkloads}</TableCell>
                    <TableCell>{queue.status?.pendingWorkloads}</TableCell>
                    <TableCell>{queue.status?.reservingWorkloads}</TableCell>
                    {resourceNames.map(resName => {
                      let total = 0;
                      (queue.status?.flavorsUsage || []).forEach(f => {
                        (f.resources || []).forEach(r => {
                          if (String(r.name) === resName) total += toNumber(r.total);
                        });
                      });
                      return (
                        <TableCell key={resName}>
                          {total > 0 ? formatResourceValue(total, resName) : '-'}
                        </TableCell>
                      );
                    })}
                    <TableCell align="right">
                      <Box display="flex" justifyContent="flex-end">
                        <ViewYamlButton
                          resourceType="localqueue"
                          resourceName={queue.name}
                          namespace={queue.namespace}
                        />
                      </Box>
                    </TableCell>
                  </TableRow>
                ))
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Paper>
  );
};

export default LocalQueues;
