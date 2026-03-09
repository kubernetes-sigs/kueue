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
import { ResponsiveTreeMap } from '@nivo/treemap';
import useWebSocket from './useWebSocket';
import './App.css';
import ErrorMessage from './ErrorMessage';

const TopologyDetail = () => {
  const { topologyName } = useParams();
  const url = `/ws/topology/${topologyName}`;
  const { data: topologyData, error } = useWebSocket(url);

  const [topology, setTopology] = useState(null);

  useEffect(() => {
    if (topologyData && topologyData.name) {
      setTopology(topologyData);
    }
  }, [topologyData]);

  if (error) return <ErrorMessage error={error} />;

  if (!topology) {
    return (
      <Paper className="parentContainer">
        <Typography variant="h6">Loading...</Typography>
        <CircularProgress />
      </Paper>
    );
  }

  const hasDomainTree = topology.domainTree && topology.domainTree.children && topology.domainTree.children.length > 0;

  return (
    <Paper className="parentContainer">
      <Typography variant="h4" gutterBottom>Topology Detail: {topologyName}</Typography>

      <Box mt={3} width="100%">
        <Typography variant="h5" gutterBottom>Topology Map</Typography>
        {hasDomainTree ? (
          <Box sx={{ height: 400, width: '100%' }}>
            <ResponsiveTreeMap
              data={topology.domainTree}
              identity="name"
              value="value"
              tile="squarify"
              leavesOnly={false}
              innerPadding={3}
              outerPadding={6}
              label={node => node.data.name}
              colorBy="treeDepth"
              borderWidth={2}
              tooltip={({ node }) => (
                <span style={{ padding: '4px 8px', background: 'white', border: '1px solid #ccc', borderRadius: 4 }}>
                  {node.data.level}: {node.data.name} ({node.value} {node.value === 1 ? 'node' : 'nodes'})
                </span>
              )}
            />
          </Box>
        ) : (
          <Typography>No nodes found matching this topology.</Typography>
        )}
      </Box>

      <Box mt={5} width="100%">
        <Typography variant="h5" gutterBottom>Levels</Typography>
        {topology.levels && topology.levels.length > 0 ? (
          <TableContainer component={Paper} className="tableContainerWithBorder">
            <Table>
              <TableHead>
                <TableRow>
                  <TableCell>Level</TableCell>
                  <TableCell>Node Label</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {topology.levels.map((level, index) => (
                  <TableRow key={index}>
                    <TableCell>{index + 1}</TableCell>
                    <TableCell>{level.nodeLabel}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TableContainer>
        ) : (
          <Typography>No levels defined.</Typography>
        )}
      </Box>

      <Box mt={5} width="100%">
        <Typography variant="h5" gutterBottom>Resource Flavors Using This Topology</Typography>
        {topology.resourceFlavors && topology.resourceFlavors.length === 0 ? (
          <Typography>No resource flavors reference this topology.</Typography>
        ) : (
          <TableContainer component={Paper} className="tableContainerWithBorder">
            <Table>
              <TableHead>
                <TableRow>
                  <TableCell>Resource Flavor Name</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {topology.resourceFlavors && topology.resourceFlavors.map((rf) => (
                  <TableRow key={rf.name}>
                    <TableCell>
                      <Link to={`/resource-flavor/${rf.name}`}>{rf.name}</Link>
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

export default TopologyDetail;
