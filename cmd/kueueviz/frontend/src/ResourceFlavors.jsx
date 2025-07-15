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

const ResourceFlavors = () => {
  const { data: flavors, error } = useWebSocket('/ws/resource-flavors');
  const [resourceFlavors, setResourceFlavors] = useState([]);

  useEffect(() => {
    if (flavors && Array.isArray(flavors)) {
      setResourceFlavors(flavors);
    }
  }, [flavors]);

  if (error) return <ErrorMessage error={error} />;

  return (
    <Paper className="parentContainer">
      <Typography variant="h4" gutterBottom>Resource Flavors</Typography>
      {resourceFlavors.length === 0 ? (
        <Typography>No Resource Flavors found.</Typography>
      ) : (
        <TableContainer component={Paper} className="tableContainerWithBorder">
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Name</TableCell>
                <TableCell>Details</TableCell>
                <TableCell align="right">Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {resourceFlavors.map((flavor) => (
                <TableRow key={flavor.name}>
                  <TableCell>
                    <Link to={`/resource-flavor/${flavor.name}`}>{flavor.name}</Link>
                  </TableCell>
                  <TableCell>{JSON.stringify(flavor.details)}</TableCell>
                  <TableCell align="right">
                    <Box display="flex" justifyContent="flex-end">
                      <ViewYamlButton 
                        resourceType="resourceflavor"
                        resourceName={flavor.name}
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

export default ResourceFlavors;
