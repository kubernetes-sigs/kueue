/*
Copyright The Kubernetes Authors.

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
import { useNavigate } from 'react-router-dom';
import { Box, Button, Container, IconButton, Paper, TextField, Tooltip, Typography } from '@mui/material';
import ContentCopyIcon from '@mui/icons-material/ContentCopy';
import { useAuth } from './AuthContext';

const Login = () => {
  const [tokenInput, setTokenInput] = useState('');
  const [copied, setCopied] = useState(false);
  const { login } = useAuth();
  const navigate = useNavigate();

  const tokenCommand = 'kubectl create token <service-account> -n default --duration=168h';

  const handleCopy = () => {
    navigator.clipboard.writeText(tokenCommand);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  const handleSubmit = (e) => {
    e.preventDefault();
    const trimmed = tokenInput.trim();
    if (!trimmed) {
      return;
    }
    login(trimmed);
    navigate('/');
  };

  return (
    <Container maxWidth="md" sx={{ mt: 8 }}>
      <Paper elevation={3} sx={{ p: 4 }}>
        <Typography variant="h5" gutterBottom>
          KueueViz Authentication
        </Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          Enter a Kubernetes bearer token to access the dashboard.
        </Typography>
        <Box sx={{ mb: 3, display: 'flex', alignItems: 'center', bgcolor: 'grey.100', borderRadius: 1, p: 1 }}>
          <Typography variant="body2" sx={{ fontFamily: 'monospace', flexGrow: 1, textAlign: 'left' }}>
            {tokenCommand}
          </Typography>
          <Tooltip title={copied ? 'Copied!' : 'Copy command'}>
            <IconButton size="small" onClick={handleCopy}>
              <ContentCopyIcon fontSize="small" />
            </IconButton>
          </Tooltip>
        </Box>
        <Box component="form" onSubmit={handleSubmit}>
          <TextField
            fullWidth
            label="Bearer Token"
            type="password"
            value={tokenInput}
            onChange={(e) => setTokenInput(e.target.value)}
            sx={{ mb: 2 }}
          />
          <Button type="submit" variant="contained" fullWidth disabled={!tokenInput.trim()}>
            Sign In
          </Button>
        </Box>
      </Paper>
    </Container>
  );
};

export default Login;
