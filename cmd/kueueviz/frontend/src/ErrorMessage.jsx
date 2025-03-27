/*
Copyright 2025 The Kubernetes Authors.

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

import React, { useState } from 'react';
import { Typography, Button, Collapse, Box, Paper } from '@mui/material';
import KeyboardArrowDownIcon from '@mui/icons-material/KeyboardArrowDown';
import KeyboardArrowUpIcon from '@mui/icons-material/KeyboardArrowUp';
import './App.css';

const ErrorMessage = ({ error }) => {
  const [expanded, setExpanded] = useState(false);
  
  if (!error) return null;

  // Extract the first line as the summary
  const lines = error.split('\n');
  const errorSummary = lines[0];
  
  // The rest is considered details
  const errorDetails = lines.slice(1).join('\n');
  
  // Format for HTML display
  const formattedDetails = errorDetails.replace(/\n/g, '<br />');

  return (
    <Paper className="error-message" elevation={2}>
      <Typography variant="h6" color="error" gutterBottom>
        Error
      </Typography>
      <Typography variant="body1" color="error.dark">
        {errorSummary}
      </Typography>
      
      {errorDetails && (
        <Box className="error-details-container">
          <Button 
            variant="text" 
            color="primary"
            onClick={() => setExpanded(!expanded)}
            endIcon={expanded ? <KeyboardArrowUpIcon /> : <KeyboardArrowDownIcon />}
            size="small"
          >
            {expanded ? "Hide Details" : "Show Details"}
          </Button>
          
          <Collapse in={expanded}>
            <Box className="error-details-content">
              <Typography 
                variant="body2" 
                color="textSecondary" 
                component="div"
                className="error-details-text"
                dangerouslySetInnerHTML={{ __html: formattedDetails }} 
              />
            </Box>
          </Collapse>
        </Box>
      )}
    </Paper>
  );
};

export default ErrorMessage; 