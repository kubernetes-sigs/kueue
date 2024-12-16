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

import { Grid, Paper, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, Tooltip, Typography, IconButton, Collapse, Button } from '@mui/material';
import React, { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { toast, ToastContainer } from 'react-toastify';
import KeyboardArrowDownIcon from '@mui/icons-material/KeyboardArrowDown';
import KeyboardArrowUpIcon from '@mui/icons-material/KeyboardArrowUp';
import useWebSocket from './useWebSocket';
import './App.css';
import { AccessTime, Check, CheckBox, CheckCircle } from '@mui/icons-material';

const Dashboard = () => {
  const [queues, setQueues] = useState([]);
  const [workloads, setWorkloads] = useState([]);
  const [expandedRows, setExpandedRows] = useState({});
  const [expandAll, setExpandAll] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);
  const [workloadsByUid, setWorkloadsByUid] = useState({});

  const { data: kueueData, error: kueueError } = useWebSocket('/ws/workloads/dashboard');

  useEffect(() => {
    if (kueueData) {
      setQueues(kueueData.queues?.items || []);
      setWorkloads(kueueData.workloads?.items || []);
      setWorkloadsByUid(kueueData.workloads.workloads_by_uid || {});

      kueueData.workloads?.items?.forEach(workload => {
        if (workload.preemption?.preempted) {
          toast.error(`Workload ${workload.metadata?.name} was preempted: ${workload.preemption.reason}`);
        }
      });
    }
    if (kueueError) setError("Failed to fetch data from WebSocket");
    setLoading(false);
  }, [kueueData, kueueError]);

  const toggleRow = (workloadName) => {
    setExpandedRows((prevExpandedRows) => ({
      ...prevExpandedRows,
      [workloadName]: !prevExpandedRows[workloadName],
    }));
  };

  const toggleExpandAll = () => {
    const newExpandAll = !expandAll;
    setExpandAll(newExpandAll);
    const newExpandedRows = {};
    workloads.forEach(workload => {
      newExpandedRows[workload.metadata?.name] = newExpandAll;
    });
    setExpandedRows(newExpandedRows);
  };

  const formatPreemptedTextWithTooltip = (preemptedCondition) => {
    if (!preemptedCondition) return "";

    const preemptedText = `${preemptedCondition.type}: ${preemptedCondition.message || ""}`;

    // Match UID pattern in the message
    const uidPattern = /\(UID: ([0-9a-fA-F-]+)\)/;
    const parts = preemptedText.split(uidPattern);

    return (
      <Tooltip
        title={
          parts.map((part, index) => (
            workloadsByUid[part] ? (
              <Link key={index} to={`/workload/${workloadsByUid[part]}`}>
                (UID: {part})
              </Link>
            ) : (
              <span key={index}>{part}</span>
            )
          ))
        }
        arrow
      >{preemptedCondition.type}
      </Tooltip>
    );
  };

  if (loading) return <Typography variant="h6">Loading...</Typography>;
  if (error) return <Typography variant="h6" color="error">{error}</Typography>;

  return (
    <>
      <ToastContainer />
      <Grid container spacing={4}>
        <Grid item xs={12} sm={6} md={4}>
          <Paper elevation={3} style={{ padding: '16px' }}>
            <Typography variant="h6">Total Local Queues</Typography>
            <Typography variant="h3">{queues.length}</Typography>
          </Paper>
        </Grid>

        <Grid item xs={12} sm={6} md={4}>
          <Paper elevation={3} style={{ padding: '16px' }}>
            <Typography variant="h6">Total Workloads</Typography>
            <Typography variant="h3">{workloads.length}</Typography>
          </Paper>
        </Grid>

        <Grid item xs={12} sm={6} md={4}>
          <Paper elevation={3} style={{ padding: '16px' }}>
            <Typography variant="h6">Completed Workloads</Typography>
            <Typography variant="h3">
              {workloads.filter(wl =>
                wl.status?.conditions?.some(
                  condition => condition.type === "Finished" && condition.status === "True"
                )
              ).length}
            </Typography>
          </Paper>
        </Grid>
      </Grid>

      <Typography variant="h5" gutterBottom style={{ marginTop: '20px' }}>
        Workloads
      </Typography>
      <TableContainer component={Paper}>
        <Table>
          <TableHead>
            <TableRow>
              <TableCell className="name-column">Namespace</TableCell>
              <TableCell className="icon-column">
                <IconButton variant="contained" color="primary" onClick={toggleExpandAll}>
                  {expandAll ? <KeyboardArrowUpIcon /> : <KeyboardArrowDownIcon />}
                </IconButton>
              </TableCell>
              <TableCell className="name-column">Name</TableCell>
              <TableCell className="pods-count-column">Pods Count</TableCell>
              <TableCell className="status-column">Status</TableCell>
              <TableCell className="queue-name-column">Queue Name</TableCell>
              <TableCell className="admission-status-column">Admission Status</TableCell>
              <TableCell className="cluster-queue-column">Cluster Queue Admission</TableCell>
              <TableCell className="preempted-column">Preemption</TableCell>
              <TableCell className="priority-column">Priority</TableCell>
              <TableCell className="priority-class-column">Priority Class Name</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {workloads.map((workload) => {
              const podCount = workload.spec?.podSets?.reduce((sum, podSet) => sum + (podSet.count || 0), 0) || 0;
              const isExpanded = expandedRows[workload.metadata.name];
              const pods = workload.pods || [];

              const preemptedCondition = workload.status?.conditions?.find(cond => cond.reason === "Preempted" && cond.status === "True");
              // Determine Finished status
              const finishedCondition = workload.status?.conditions?.find(
                cond => cond.type === "Finished" && cond.status === "True"
              );
              const statusIcon = finishedCondition ? <CheckCircle style={{ color: "green" }} /> : <AccessTime style={{ color: "orange" }} />;
              return (
                <React.Fragment key={workload.metadata.name}>
                  <TableRow>
                    <TableCell className="name-column">{workload.metadata.namespace}</TableCell>
                    <TableCell className="icon-column">
                      <IconButton onClick={() => toggleRow(workload.metadata.name)}>
                        {isExpanded ? <KeyboardArrowUpIcon /> : <KeyboardArrowDownIcon />}
                      </IconButton>
                    </TableCell>
                    <TableCell className="name-column">
                      <Link to={`/workload/${workload.metadata.namespace}/${workload.metadata.name}`}>
                        {workload.metadata.name}
                      </Link>
                    </TableCell>
                    <TableCell className="pods-count-column">{podCount}</TableCell>
                    <TableCell className="status-column">{statusIcon}</TableCell>
                    <TableCell className="queue-name-column">
                      <Link to={`/local-queue/${workload.metadata.namespace}/${workload.spec.queueName}`}>{workload.spec.queueName}</Link>
                    </TableCell>
                    <TableCell className="admission-status-column">
                      {(() => {
                        const admittedCondition = workload.status?.conditions?.find(cond => cond.type === "Admitted");
                        const admissionStatus = admittedCondition && admittedCondition.status === "True" ? "" : "Not admitted:";
                        return `${admissionStatus} ${admittedCondition?.reason || "N/A"}`;
                      })()}
                    </TableCell>
                    <TableCell className="cluster-queue-column">
                      <Link to={`/cluster-queue/${workload.status?.admission?.clusterQueue}`}>
                        {workload.status?.admission?.clusterQueue || "N/A"}
                      </Link>
                    </TableCell>
                    <TableCell className="preempted-column">
                      {formatPreemptedTextWithTooltip(preemptedCondition)}
                    </TableCell>
                    <TableCell className="priority-column">{workload.spec.priority || "N/A"}</TableCell>
                    <TableCell className="priority-class-column">{workload.spec.priorityClassName || "N/A"}</TableCell>
                  </TableRow>
                  <TableRow>
                    <TableCell colSpan={12} style={{ paddingBottom: 0, paddingTop: 0 }}>
                      <Collapse in={isExpanded} timeout="auto" unmountOnExit>
                        <Table size="small">
                          <TableHead>
                            <TableRow>
                              <TableCell className="icon-column"></TableCell>
                              <TableCell className="tree-indicator-column">|</TableCell>
                              <TableCell className="pod-name-column">Pod Name</TableCell>
                              <TableCell className="pod-status-column">Status</TableCell>
                              <TableCell className="pod-reason-column">Pending Reason</TableCell>
                              <TableCell className=""></TableCell>
                            </TableRow>
                          </TableHead>
                          <TableBody>
                            {pods.map((pod) => {
                              const podPhase = pod.status?.phase;
                              const pendingCondition = pod.status?.conditions?.find(
                                cond => cond.type === "PodScheduled" && cond.status === "False"
                              );
                              return (
                                <TableRow key={pod.name}>
                                  <TableCell className="icon-column"></TableCell>
                                  <TableCell className="tree-indicator-column"><span style={{ color: '#888' }}>⎯</span></TableCell>
                                  <TableCell className="pod-name-column">{pod.name}</TableCell>
                                  <TableCell className="pod-status-column">{podPhase}</TableCell>
                                  <TableCell className="pod-reason-column">
                                    {podPhase === "Pending" && pendingCondition ? (
                                      <Tooltip title={`${pendingCondition.reason}: ${pendingCondition.message}`}>
                                        <Typography color="error">{pendingCondition.reason}</Typography>
                                      </Tooltip>
                                    ) : (
                                      "N/A"
                                    )}
                                  </TableCell>
                                  <TableCell className="pod-name-column"></TableCell>
                                  <TableCell className="pod-name-column"></TableCell>
                                  <TableCell className="pod-name-column"></TableCell>
                                </TableRow>
                              );
                            })}
                          </TableBody>
                        </Table>
                      </Collapse>
                    </TableCell>
                  </TableRow>
                </React.Fragment>
              );
            })}
          </TableBody>
        </Table>
      </TableContainer>
    </>
  );
};

export default Dashboard;
