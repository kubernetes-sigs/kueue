import { CircularProgress, Paper, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, Typography } from '@mui/material';
import React, { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import useWebSocket from './useWebSocket';
import './App.css';

const LocalQueues = () => {
  const { data: localQueues, error } = useWebSocket('/ws/local-queues');
  const [queues, setQueues] = useState([]);

  useEffect(() => {
    if (localQueues && Array.isArray(localQueues)) {
      setQueues(localQueues);
    }
  }, [localQueues]);

  if (error) return <Typography color="error">{error}</Typography>;

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
    <Paper style={{ padding: '16px', marginTop: '20px' }}>
      <Typography variant="h4" gutterBottom>Local Queues</Typography>
      {queues.length === 0 ? (
        <Typography>No Local Queues found.</Typography>
      ) : (
        <TableContainer component={Paper}>
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Namespace</TableCell>
                <TableCell>Name</TableCell>
                <TableCell>Cluster Queue</TableCell>
                <TableCell>Admitted Workloads</TableCell>
                <TableCell>Pending Workloads</TableCell>
                <TableCell>Reserving Workloads</TableCell>
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
