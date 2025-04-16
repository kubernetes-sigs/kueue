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

import React, { useEffect, useState } from 'react';
import { useParams, Link } from 'react-router-dom'; 
import { Typography,  Paper, CircularProgress, Grid, Table, TableBody, TableCell, TableContainer, TableHead, TableRow } from '@mui/material';
import useWebSocket from './useWebSocket';
import './App.css';
import ErrorMessage from './ErrorMessage';

const WorkloadDetail = () => {
  const { namespace, workloadName } = useParams();
  const workloadUrl = `/ws/workload/${namespace}/${workloadName}`;
  const eventsUrl = `/ws/workload/${namespace}/${workloadName}/events`;

  const { data: workload, error: workloadError } = useWebSocket(workloadUrl);
  const { data: eventData, error: eventError } = useWebSocket(eventsUrl);

  const [events, setEvents] = useState([]);

  useEffect(() => {
    if (eventData && Array.isArray(eventData)) {
      const sortedEvents = eventData.sort((a, b) => new Date(b.timestamp) - new Date(a.timestamp));
      setEvents(sortedEvents);
    }
  }, [eventData]);

  if (!workload) return <CircularProgress />;
  if (workloadError) return <ErrorMessage error={workloadError} />;

  return (
    <Paper style={{ padding: '16px', marginTop: '20px' , alignContent: 'left', alignItems: 'left' }}>
      <Typography variant="h4" gutterBottom>Workload Detail: {namespace}/{workloadName}</Typography>
      <Grid container spacing={2}>
        <Grid item xs={12} sm={6}>
          <Typography variant="body1">
            <strong>Queue Name:</strong>
            <Link to={`/local-queue/${namespace}/${workload.spec?.queueName}`}>{workload.spec?.queueName}</Link>
            {" → "}
            <Link to={`/cluster-queue/${workload.clusterQueueName}`}>{workload.clusterQueueName}</Link>
          </Typography>
          <Typography variant="body1"><strong>Status:</strong> {workload.status?.state || 'Unknown'}</Typography>
          <Typography variant="body1"><strong>Priority:</strong> {workload.spec?.priority || 'N/A'}</Typography>
          <Typography variant="body1"><strong>Priority Class Name:</strong> {workload.spec?.priorityClassName || 'N/A'}</Typography>
        </Grid>
        <Grid item xs={12} sm={6}>
          <Typography variant="body1"><strong>Owner Reference:</strong></Typography>
          <Typography variant="body2">API Version: {workload.ownerReferences?.[0]?.apiVersion || 'N/A'}</Typography>
          <Typography variant="body2">Kind: {workload.ownerReferences?.[0]?.kind || 'N/A'}</Typography>
          <Typography variant="body2">Name: {workload.ownerReferences?.[0]?.name || 'N/A'}</Typography>
          <Typography variant="body2">UID: {workload.ownerReferences?.[0]?.uid || 'N/A'}</Typography>
        </Grid>
        <Grid item xs={12}>
          <Typography variant="body1"><strong>Pod Sets Count:</strong> {workload.spec?.podSets?.[0]?.count || 'N/A'}</Typography>
        </Grid>
      </Grid>

      <Typography variant="h5" gutterBottom style={{ marginTop: '20px' }}>
        Events
      </Typography>

      {eventError ? (
        <Typography color="error">{eventError}</Typography>
      ) : (
        <TableContainer component={Paper}>
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Timestamp</TableCell>
                <TableCell>Type</TableCell>
                <TableCell>Reason</TableCell>
                <TableCell>Message</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {events.map((event) => (
                <TableRow key={event.name}>
                  <TableCell>
                    {new Date(event.firstTimestamp).toISOString().replace('T', '@').slice(0, 19)  } UTC
                  </TableCell>
                  <TableCell>{event.type}</TableCell>
                  <TableCell>{event.reason}</TableCell>
                  <TableCell>{event.message}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Paper>
  );
};

export default WorkloadDetail;