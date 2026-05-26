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

import React, { useState, useEffect, useMemo } from 'react';
import { Link } from 'react-router-dom';
import { Typography, Paper, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, Box, ToggleButton, ToggleButtonGroup, List, ListItem, ListItemButton, ListItemText, Collapse, IconButton, Chip } from '@mui/material';
import { ExpandMore, ChevronRight, ViewList, AccountTree } from '@mui/icons-material';
import useWebSocket from './useWebSocket';
import './App.css';
import ErrorMessage from './ErrorMessage';
import ViewYamlButton from './ViewYamlButton';

// Build tree structure from flat cohort list
const buildCohortTree = (cohorts) => {
  const cohortMap = new Map();
  cohorts.forEach(c => cohortMap.set(c.name, { ...c, children: [] }));

  const roots = [];
  cohorts.forEach(c => {
    const node = cohortMap.get(c.name);
    if (c.parentName && cohortMap.has(c.parentName)) {
      cohortMap.get(c.parentName).children.push(node);
    } else {
      roots.push(node);
    }
  });
  return roots;
};

// Recursive tree node component
const CohortTreeNode = ({ cohort, depth = 0 }) => {
  const [open, setOpen] = useState(true);
  const hasChildren = cohort.children?.length > 0;
  const queues = cohort.clusterQueues || [];

  return (
    <>
      <ListItem disablePadding>
        <Box sx={{ display: 'flex', alignItems: 'center', flexGrow: 1, pl: depth * 3 }}>
          <Box sx={{ width: 28, flexShrink: 0, display: 'flex', justifyContent: 'center' }}>
            {hasChildren && (
              <IconButton size="small" onClick={() => setOpen(!open)}>
                {open ? <ExpandMore fontSize="small" /> : <ChevronRight fontSize="small" />}
              </IconButton>
            )}
          </Box>
          <ListItemButton component={Link} to={`/cohort/${cohort.name}`} sx={{ py: 1 }}>
            <ListItemText
              primary={
                <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                  <Typography variant="subtitle1" fontWeight="medium">{cohort.name}</Typography>
                  {hasChildren && (
                    <Chip label={`${cohort.children.length} ${cohort.children.length === 1 ? 'child' : 'children'}`} size="small" color="primary" variant="outlined" />
                  )}
                </Box>
              }
              secondary={`Cluster Queues: ${queues.length > 0 ? queues.map(q => q.name).join(', ') : 'None'}`}
            />
          </ListItemButton>
        </Box>
        <Box sx={{ flexShrink: 0, pr: 1 }}>
          <ViewYamlButton resourceType="cohort" resourceName={cohort.name} />
        </Box>
      </ListItem>
      {hasChildren && (
        <Collapse in={open} timeout="auto" unmountOnExit>
          <List disablePadding>
            {cohort.children.map(child => (
              <CohortTreeNode key={child.name} cohort={child} depth={depth + 1} />
            ))}
          </List>
        </Collapse>
      )}
    </>
  );
};

const Cohorts = () => {
  const { data: cohorts, error } = useWebSocket('/ws/cohorts');
  const [cohortList, setCohortList] = useState([]);
  const [viewMode, setViewMode] = useState('list');

  useEffect(() => {
    if (cohorts && Array.isArray(cohorts)) {
      setCohortList([...cohorts].sort((a, b) => (a.name || '').localeCompare(b.name || '')));
    }
  }, [cohorts]);

  const cohortTree = useMemo(() => buildCohortTree(cohortList), [cohortList]);

  if (error) return <ErrorMessage error={error} />;

  return (
    <Paper className="parentContainer">
      <Box display="flex" alignItems="center" mb={2} sx={{ width: '100%', position: 'relative' }}>
        <Typography variant="h4" sx={{ width: '100%', textAlign: 'center' }}>Cohorts</Typography>
        <ToggleButtonGroup
          value={viewMode}
          exclusive
          onChange={(e, v) => v && setViewMode(v)}
          size="small"
          sx={{ width: '100%', justifyContent: 'flex-end' }}
        >
          <ToggleButton value="list"><ViewList fontSize="small" sx={{ mr: 0.5 }} />List</ToggleButton>
          <ToggleButton value="tree"><AccountTree fontSize="small" sx={{ mr: 0.5 }} />Tree</ToggleButton>
        </ToggleButtonGroup>
      </Box>

      {cohortList.length === 0 ? (
        <Typography>No Cohorts found.</Typography>
      ) : viewMode === 'tree' ? (
        <TableContainer component={Paper} className="tableContainerWithBorder">
          <List disablePadding>
            {cohortTree.map(cohort => (
              <CohortTreeNode key={cohort.name} cohort={cohort} />
            ))}
          </List>
        </TableContainer>
      ) : (
        <TableContainer component={Paper} className="tableContainerWithBorder">
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Cohort Name</TableCell>
                <TableCell>Parent</TableCell>
                <TableCell>Number of Queues</TableCell>
                <TableCell>Cluster Queue Name</TableCell>
                <TableCell align="right">Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {cohortList.map((cohort) => (
                cohort.clusterQueues && cohort.clusterQueues.length > 0 ? (
                  cohort.clusterQueues.map((queue, index) => (
                    <TableRow key={`${cohort.name}-${queue.name}`}>
                      {index === 0 && (<>
                        <TableCell rowSpan={cohort.clusterQueues.length}>
                          <Link to={`/cohort/${cohort.name}`}>{cohort.name}</Link>
                        </TableCell>
                        <TableCell rowSpan={cohort.clusterQueues.length}>
                          {cohort.parentName ? <Link to={`/cohort/${cohort.parentName}`}>{cohort.parentName}</Link> : '-'}
                        </TableCell>
                        <TableCell rowSpan={cohort.clusterQueues.length}>
                          {cohort.clusterQueues.length}
                        </TableCell>
                      </>)}
                      <TableCell>
                        <Link to={`/cluster-queue/${queue.name}`}>{queue.name}</Link>
                      </TableCell>
                      {index === 0 && (
                        <TableCell rowSpan={cohort.clusterQueues.length} align="right">
                          <ViewYamlButton resourceType="cohort" resourceName={cohort.name} />
                        </TableCell>
                      )}
                    </TableRow>
                  ))
                ) : (
                  <TableRow key={cohort.name}>
                    <TableCell><Link to={`/cohort/${cohort.name}`}>{cohort.name}</Link></TableCell>
                    <TableCell>{cohort.parentName ? <Link to={`/cohort/${cohort.parentName}`}>{cohort.parentName}</Link> : '-'}</TableCell>
                    <TableCell>0</TableCell>
                    <TableCell>No cluster queues found for this cohort.</TableCell>
                    <TableCell align="right">
                      <ViewYamlButton resourceType="cohort" resourceName={cohort.name} />
                    </TableCell>
                  </TableRow>
                )
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Paper>
  );
};

export default Cohorts;
