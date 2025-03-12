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

import { CircularProgress, Paper, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, Tooltip, Typography } from '@mui/material';
import React, { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import 'react-toastify/dist/ReactToastify.css';
import useWebSocket from './useWebSocket';
import './App.css';

const Workloads = () => {
  const { data: data, error } = useWebSocket('/ws/workloads');
  const [workloads, setWorkloads] = useState([]);
  useEffect(() => {
    setWorkloads(data?.workloads?.items || []);
  }, [data]);

  if (error) return <Typography color="error">{error}</Typography>;

  // Group workloads by namespace
  const workloadsByNamespace = workloads.reduce((acc, workload) => {
    const namespace = workload.metadata.namespace;
    if (!acc[namespace]) {
      acc[namespace] = [];
    }
    acc[namespace].push(workload);
    return acc;
  }, {});

  return (
    <Paper style={{ padding: '16px', marginTop: '20px' }}>
      {/* Display a table with workload details */}
      <Typography variant="h5" gutterBottom style={{ marginTop: '20px' }}>Workloads</Typography>
      {workloads.length === 0 ? (
        <Typography>No Workloads found.</Typography>
      ) : (
        <TableContainer component={Paper}>
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Namespace</TableCell>
                <TableCell>Name</TableCell>
                <TableCell>Queue Name</TableCell>
                <TableCell>Status</TableCell>
                <TableCell>Preempted</TableCell>
                <TableCell>Preemption Reason</TableCell>
                <TableCell>Priority</TableCell>
                <TableCell>Priority Class Name</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {Object.entries(workloadsByNamespace).map(([namespace, workloads]) => (
                workloads.map((workload, index) => (
                  <TableRow key={workload.metadata.name}>
                    {index === 0 && (
                      <TableCell rowSpan={workloads.length}>
                        {namespace}
                      </TableCell>
                    )}
                    <TableCell>
                      <Tooltip
                        title={
                          <div>
                            <div><strong>Pod Sets Count:</strong> {workload.spec?.podSets?.[0]?.count || 'N/A'}</div>
                            <div><strong>Owner Reference: {workload.ownerReferences?.[0]?.uid || 'N/A'}</strong></div>
                            <div>API Version: {workload.ownerReferences?.[0]?.apiVersion || 'N/A'}</div>
                            <div>Kind: {workload.ownerReferences?.[0]?.kind || 'N/A'}</div>
                            <div>Name: {workload.ownerReferences?.[0]?.name || 'N/A'}</div>
                          </div>
                        }
                        arrow
                      >
                        <Link to={`/workload/${workload.metadata.namespace}/${workload.metadata.name}`}>
                          <span style={{ cursor: 'pointer', textDecoration: 'underline', color: 'blue' }}>
                            {workload.metadata.name}
                          </span>
                        </Link>
                      </Tooltip>
                    </TableCell>
                    <TableCell><Link to={`/local-queue/${workload.metadata.namespace}/${workload.spec.queueName}`}>{workload.spec.queueName}</Link></TableCell>
                    <TableCell>{workload.status?.state || "Unknown"}</TableCell>
                    <TableCell>{workload.preemption?.preempted ? "Yes" : "No"}</TableCell>
                    <TableCell>{workload.preemption?.reason || "N/A"}</TableCell>
                    <TableCell>{workload.spec.priority}</TableCell>
                    <TableCell>{workload.spec.priorityClassName}</TableCell>
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

export default Workloads;
