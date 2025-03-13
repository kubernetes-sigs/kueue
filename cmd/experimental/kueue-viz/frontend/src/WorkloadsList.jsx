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

import { Link, Paper, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, Tooltip } from '@mui/material';
import React from 'react';
import './App.css';

const WorkloadsList = ({ workloads }) => {
  return (
    <TableContainer component={Paper}>
      <Table>
        <TableHead>
          <TableRow>
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
          {workloads.map((workload) => (
            <TableRow key={workload.metadata?.name}>
              <TableCell>
                <Tooltip
                  title={
                    <div>
                      <div><strong>Pod Sets Count:</strong> {workload.spec?.podSets?.[0]?.count || 'N/A'}</div>
                      <div><strong>Owner Reference:</strong> {workload.ownerReferences?.[0]?.uid || 'N/A'}</div>
                      <div>API Version: {workload.ownerReferences?.[0]?.apiVersion || 'N/A'}</div>
                      <div>Kind: {workload.ownerReferences?.[0]?.kind || 'N/A'}</div>
                      <div>Name: {workload.ownerReferences?.[0]?.name || 'N/A'}</div>
                    </div>
                  }
                  arrow
                >
                  <Link to={`/workload/${workload.metadata?.name || ''}`}>
                    <span style={{ cursor: 'pointer', textDecoration: 'underline', color: 'blue' }}>
                      {workload.metadata?.name || 'Unnamed'}
                    </span>
                  </Link>
                </Tooltip>
              </TableCell>
              <TableCell>
                {workload.spec?.queueName ? (
                  <Link to={`/local-queue/${workload.metadata.namespace}/${workload.spec.queueName}`}>
                    {workload.spec.queueName}
                  </Link>
                ) : (
                  'N/A'
                )}
              </TableCell>
              <TableCell>{workload.status?.state || "Unknown"}</TableCell>
              <TableCell>{workload.preemption?.preempted ? "Yes" : "No"}</TableCell>
              <TableCell>{workload.preemption?.reason || "N/A"}</TableCell>
              <TableCell>{workload.spec?.priority || "N/A"}</TableCell>
              <TableCell>{workload.spec?.priorityClassName || "N/A"}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </TableContainer>
  );
};

export default WorkloadsList;
