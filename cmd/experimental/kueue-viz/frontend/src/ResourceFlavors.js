import React, { useState, useEffect } from 'react';
import { Link } from 'react-router-dom';
import { Typography, Paper, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, CircularProgress } from '@mui/material';
import useWebSocket from './useWebSocket';
import './App.css';

const ResourceFlavors = () => {
  const { data: flavors, error } = useWebSocket('/ws/resource-flavors');
  const [resourceFlavors, setResourceFlavors] = useState([]);

  useEffect(() => {
    if (flavors && Array.isArray(flavors)) {
      setResourceFlavors(flavors);
    }
  }, [flavors]);

  if (error) return <Typography color="error">{error}</Typography>;

  return (
    <Paper style={{ padding: '16px', marginTop: '20px' }}>
      <Typography variant="h4" gutterBottom>Resource Flavors</Typography>
      {resourceFlavors.length === 0 ? (
        <CircularProgress />
      ) : (
        <TableContainer component={Paper}>
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Name</TableCell>
                <TableCell>Details</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {resourceFlavors.map((flavor) => (
                <TableRow key={flavor.name}>
                  <TableCell>
                    <Link to={`/resource-flavor/${flavor.name}`}>{flavor.name}</Link>
                  </TableCell>
                  <TableCell>{JSON.stringify(flavor.details)}</TableCell>
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
