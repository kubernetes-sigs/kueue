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
import { Typography, Paper, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, Box } from '@mui/material';
import useWebSocket from './useWebSocket';
import './App.css';
import ErrorMessage from './ErrorMessage';
import ViewYamlButton from './ViewYamlButton';

const Topologies = () => {
  const { data: topologies, error } = useWebSocket('/ws/topologies');
  const [topologyList, setTopologyList] = useState([]);

  useEffect(() => {
    if (topologies && Array.isArray(topologies)) {
      setTopologyList(topologies);
    }
  }, [topologies]);

  if (error) return <ErrorMessage error={error} />;

  return (
    <Paper className="parentContainer">
      <Typography variant="h4" gutterBottom>Topologies</Typography>
      {topologyList.length === 0 ? (
        <Typography>No Topologies found.</Typography>
      ) : (
        <TableContainer component={Paper} className="tableContainerWithBorder">
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Topology Name</TableCell>
                <TableCell>Levels</TableCell>
                <TableCell>Resource Flavors</TableCell>
                <TableCell align="right">Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {topologyList.map((topology) => (
                <TableRow key={topology.name}>
                  <TableCell>
                    <Link to={`/topology/${topology.name}`}>{topology.name}</Link>
                  </TableCell>
                  <TableCell>
                    {topology.levels && topology.levels.length > 0 ? (
                      topology.levels.map((level, index) => (
                        <span key={index}>
                          {level.nodeLabel}{index < topology.levels.length - 1 ? ' → ' : ''}
                        </span>
                      ))
                    ) : (
                      'No levels'
                    )}
                  </TableCell>
                  <TableCell>
                    {topology.resourceFlavors && topology.resourceFlavors.length > 0 ? (
                      topology.resourceFlavors.map((name, index) => (
                        <span key={name}>
                          <Link to={`/resource-flavor/${name}`}>{name}</Link>
                          {index < topology.resourceFlavors.length - 1 ? ', ' : ''}
                        </span>
                      ))
                    ) : (
                      'None'
                    )}
                  </TableCell>
                  <TableCell align="right">
                    <Box display="flex" justifyContent="flex-end">
                      <ViewYamlButton
                        resourceType="topology"
                        resourceName={topology.name}
                      />
                    </Box>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Paper>
  );
};

export default Topologies;
