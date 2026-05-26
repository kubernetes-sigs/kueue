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
import { Typography, Paper, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, CircularProgress, Box, Switch, FormControlLabel, Chip } from '@mui/material';
import useWebSocket from './useWebSocket';
import './App.css';
import ErrorMessage from './ErrorMessage';

const ResourceFlavorDetail = () => {
  const { flavorName } = useParams();
  const url = `/ws/resource-flavor/${flavorName}`;
  const { data: flavorData, error } = useWebSocket(url);

  const [flavor, setFlavor] = useState(null);
  const [showLabels, setShowLabels] = useState(false);

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
  const sortedNodes = [...(nodes || [])].sort((a, b) => (a.name || '').localeCompare(b.name || ''));
  const sortedQueues = [...(queues || [])].sort((a, b) => (a.queueName || '').localeCompare(b.queueName || ''));

  return (
    <Paper className="parentContainer">
      <Typography variant="h4" gutterBottom>Resource Flavor Detail: {flavorName}</Typography>
      {/* Display Flavor Details */}
      <Box mt={3} width="100%">
        <Typography variant="h5" gutterBottom>Flavor Details</Typography>
        <Box display="flex" flexDirection="column" gap={1.5}>
          <Box>
            <Typography variant="body2" color="text.secondary" gutterBottom><strong>Node Labels</strong></Typography>
            {details.nodeLabels && Object.keys(details.nodeLabels).length > 0 ? (
              <Box display="flex" flexWrap="wrap" gap={0.5}>
                {Object.entries(details.nodeLabels).map(([key, value]) => (
                  <Chip key={key} label={`${key}: ${value}`} size="small" variant="outlined" />
                ))}
              </Box>
            ) : (
              <Typography variant="body2" color="text.secondary">None</Typography>
            )}
          </Box>
          <Box>
            <Typography variant="body2" color="text.secondary" gutterBottom><strong>Node Taints</strong></Typography>
            {details.nodeTaints?.length ? (
              <Box display="flex" flexWrap="wrap" gap={0.5}>
                {details.nodeTaints.map((taint, index) => (
                  <Chip key={index} label={`${taint.key}=${taint.value} (${taint.effect})`} size="small" color="warning" variant="outlined" />
                ))}
              </Box>
            ) : (
              <Typography variant="body2" color="text.secondary">None</Typography>
            )}
          </Box>
          <Box>
            <Typography variant="body2" color="text.secondary" gutterBottom><strong>Tolerations</strong></Typography>
            {details.tolerations?.length ? (
              <Box display="flex" flexWrap="wrap" gap={0.5}>
                {details.tolerations.map((toleration, index) => (
                  <Chip key={index} label={`${toleration.key} (${toleration.operator}): ${toleration.effect}`} size="small" variant="outlined" />
                ))}
              </Box>
            ) : (
              <Typography variant="body2" color="text.secondary">None</Typography>
            )}
          </Box>
        </Box>
      </Box>

      {/* Display Queues Using This Flavor */}
      <Box mt={5} width="100%">
        <Typography variant="h5" gutterBottom>
          Queues Using This Flavor {sortedQueues.length > 0 && <Chip label={sortedQueues.length} size="small" sx={{ ml: 1 }} />}
        </Typography>
        {sortedQueues.length === 0 ? (
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
                {sortedQueues.map((queue) => (
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
        <Box display="flex" alignItems="center" gap={2} mb={1}>
          <Typography variant="h5" sx={{ mb: 0 }}>
            Nodes Matching This Flavor {sortedNodes.length > 0 && <Chip label={sortedNodes.length} size="small" sx={{ ml: 1 }} />}
          </Typography>
          <FormControlLabel
            control={<Switch checked={showLabels} onChange={(e) => setShowLabels(e.target.checked)} size="small" />}
            label="Show Labels"
            sx={{ ml: 1 }}
          />
        </Box>
        {sortedNodes.length === 0 ? (
          <Typography>No nodes match this flavor.</Typography>
        ) : (
          <TableContainer component={Paper} className="tableContainerWithBorder">
            <Table size="small">
              <TableHead>
                <TableRow>
                  <TableCell>Node Name</TableCell>
                  {showLabels && <TableCell>Labels</TableCell>}
                  <TableCell>Taints</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {sortedNodes.map((node) => (
                  <TableRow key={node.name}>
                    <TableCell>{node.name}</TableCell>
                    {showLabels && (
                      <TableCell>
                        <Box display="flex" flexWrap="wrap" gap={0.5}>
                          {Object.entries(node.labels).map(([key, value]) => (
                            <Chip key={key} label={`${key}: ${value}`} size="small" variant="outlined" />
                          ))}
                        </Box>
                      </TableCell>
                    )}
                    <TableCell>
                      {node.taints?.length > 0 ? (
                        <Box display="flex" flexWrap="wrap" gap={0.5}>
                          {node.taints.map((taint, index) => (
                            <Chip key={index} label={`${taint.key}=${taint.value} (${taint.effect})`} size="small" color="warning" variant="outlined" />
                          ))}
                        </Box>
                      ) : (
                        <Typography variant="body2" color="text.secondary">—</Typography>
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
