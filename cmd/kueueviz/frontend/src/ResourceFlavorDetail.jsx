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
import { useParams, Link } from 'react-router-dom';
import { Typography, Paper, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, CircularProgress, Box } from '@mui/material';
import useWebSocket from './useWebSocket';
import './App.css';
import ErrorMessage from './ErrorMessage';

const ResourceFlavorDetail = () => {
  const { flavorName } = useParams();
  const url = `/ws/resource-flavor/${flavorName}`;
  const { data: flavorData, error } = useWebSocket(url);

  const [flavor, setFlavor] = useState(null);

  useEffect(() => {
    if (flavorData && flavorData.name) {
      console.log("Received flavor data:", flavorData);
      setFlavor(flavorData);
    }
  }, [flavorData]);

  if (error) return <ErrorMessage error={error} />;

  if (!flavor) {
    return (
      <Paper className="parentContainer">
        <Typography variant="h6">Loading...</Typography>
        <CircularProgress />
      </Paper>
    );
  }

  const { details, queues, nodes } = flavor;

  return (
    <Paper className="parentContainer">
      <Typography variant="h4" gutterBottom>Resource Flavor Detail: {flavorName}</Typography>
      {/* Display Flavor Details */}
      <Box mt={3} width="100%">
        <Typography variant="h5" gutterBottom>Flavor Details</Typography>
        <Typography variant="body1"><strong>Node Labels:</strong></Typography>
        {details.nodeLabels ? (
          <ul>
            {Object.entries(details.nodeLabels)?.map(([key, value]) => (
              <li key={key}>{key}: {value}</li>
            ))}
          </ul>
        ) : (
          <Typography variant="body2">No specific node labels.</Typography>
        )}

        <Typography variant="body1" mt={2}><strong>Node Taints:</strong></Typography>
        {details.nodeTaints?.length ? (
          <ul>
            {details.nodeTaints?.map((taint, index) => (
              <li key={index}>
                {taint.key}={taint.value} ({taint.effect})
              </li>
            ))}
          </ul>
        ) : (
          <Typography variant="body2">No specific node taints.</Typography>
        )}

        <Typography variant="body1" mt={2}><strong>Tolerations:</strong></Typography>
        {details.tolerations?.length ? (
          <ul>
            {details.tolerations?.map((toleration, index) => (
              <li key={index}>
                {toleration.key} ({toleration.operator}): {toleration.effect}
              </li>
            ))}
          </ul>
        ) : (
          <Typography variant="body2">No specific tolerations.</Typography>
        )}
      </Box>

      {/* Display Queues Using This Flavor */}
      <Box mt={5} width="100%">
        <Typography variant="h5" gutterBottom>Queues Using This Flavor</Typography>
        {queues && queues.length === 0 ? (
          <Typography>No cluster queues are using this flavor.</Typography>
        ) : (
          <TableContainer component={Paper} className="tableContainerWithBorder">
            <Table>
              <TableHead>
                <TableRow>
                  <TableCell>Cluster Queue Name</TableCell>
                  <TableCell>Resource</TableCell>
                  <TableCell>Nominal Quota</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {queues?.map((queue) => (
                  <React.Fragment key={queue.queueName}>
                    <TableRow>
                      <TableCell rowSpan={queue.quota.length}>
                        <Link to={`/cluster-queue/${queue.queueName}`}>{queue.queueName}</Link>
                      </TableCell>
                      <TableCell>{queue.quota[0].resource}</TableCell>
                      <TableCell>{queue.quota[0].nominalQuota}</TableCell>
                    </TableRow>
                    {queue.quota.slice(1)?.map((resource, index) => (
                      <TableRow key={`${queue.queueName}-${resource.resource}-${index}`}>
                        <TableCell>{resource.resource}</TableCell>
                        <TableCell>{resource.nominalQuota}</TableCell>
                      </TableRow>
                    ))}
                  </React.Fragment>
                ))}
              </TableBody>
            </Table>
          </TableContainer>
        )}
      </Box>

      {/* Display Nodes Matching This Flavor */}
      <Box mt={5} width="100%">
        <Typography variant="h5" gutterBottom>Nodes Matching This Flavor</Typography>
        {nodes && nodes.length === 0 ? (
          <Typography>No nodes match this flavor.</Typography>
        ) : (
          <TableContainer component={Paper} className="tableContainerWithBorder">
            <Table>
              <TableHead>
                <TableRow>
                  <TableCell>Node Name</TableCell>
                  <TableCell>Labels</TableCell>
                  <TableCell>Taints</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {nodes?.map((node) => (
                  <TableRow key={node.name}>
                    <TableCell>{node.name}</TableCell>
                    <TableCell>
                      <ul>
                        {Object.entries(node.labels).map(([key, value]) => (
                          <li key={key}>{key}: {value}</li>
                        ))}
                      </ul>
                    </TableCell>
                    <TableCell>
                      {node.taints.length > 0 ? (
                        <ul>
                          {node.taints.map((taint, index) => (
                            <li key={index}>
                              {taint.key}={taint.value} ({taint.effect})
                            </li>
                          ))}
                        </ul>
                      ) : (
                        <Typography variant="body2">No taints</Typography>
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TableContainer>
        )}
      </Box>
    </Paper>
  );
};

export default ResourceFlavorDetail;
