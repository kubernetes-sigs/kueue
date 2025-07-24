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

import React, { useState, useEffect, useRef, useCallback } from 'react';
import {
  Dialog,
  DialogTitle,
  DialogContent,
  Typography,
  CircularProgress,
  Box,
  IconButton,
} from '@mui/material';
import {
  Close as CloseIcon,
} from '@mui/icons-material';
import AceEditor from 'react-ace';
import { buildResourceUrl } from './utils/urlHelper';

// Import Ace Editor modes and themes
import 'ace-builds/src-noconflict/mode-yaml';
import 'ace-builds/src-noconflict/ext-searchbox';

const useYamlFetcher = (open, resourceType, resourceName, namespace) => {
  const [yamlContent, setYamlContent] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const fetchYaml = useCallback(async () => {
    if (!open || !resourceType || !resourceName) return;

    setLoading(true);
    setError('');
    setYamlContent('');

    try {
      const apiUrl = buildResourceUrl(resourceType, resourceName, {
        namespace,
        output: 'yaml'
      });
      
      const response = await fetch(apiUrl);

      if (!response.ok) {
        throw new Error(`Failed to fetch YAML: ${response.status} ${response.statusText}`);
      }

      const data = await response.json();
      setYamlContent(data.content);
    } catch (err) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }, [open, resourceType, resourceName, namespace]);

  useEffect(() => {
    fetchYaml();
  }, [fetchYaml]);

  return { yamlContent, loading, error, refetch: fetchYaml };
};

const YamlViewer = ({ open, onClose, resourceType, resourceName, namespace = null }) => {
  const { yamlContent, loading, error } = useYamlFetcher(open, resourceType, resourceName, namespace);
  const editorRef = useRef(null);

  // Handle keyboard shortcuts
  useEffect(() => {
    const handleKeyDown = (event) => {
      if ((event.metaKey || event.ctrlKey) && event.key === 'f') {
        return;
      }
      
      // ESC to close dialog
      if (event.key === 'Escape') {
        if (open) {
          handleClose();
        }
      }
    };

    if (open) {
      document.addEventListener('keydown', handleKeyDown);
      return () => {
        document.removeEventListener('keydown', handleKeyDown);
      };
    }
  }, [open]);



  const handleClose = () => {
    onClose();
  };


  const getResourceTitle = () => {
    const resourceTypeName = resourceType.charAt(0).toUpperCase() + resourceType.slice(1);
    const namespaceStr = namespace ? ` (${namespace})` : '';
    return `${resourceTypeName}: ${resourceName}${namespaceStr}`;
  };


  return (
    <Dialog
      open={open}
      onClose={handleClose}
      maxWidth="lg"
      fullWidth
    >
      <DialogTitle>
        <Box display="flex" justifyContent="space-between" alignItems="center">
          <Typography variant="h6" sx={{ color: 'text.primary' }}>
            {getResourceTitle()}
          </Typography>
          <IconButton onClick={handleClose} size="small" sx={{color: 'text.primary'}}>
            <CloseIcon />
          </IconButton>
        </Box>
      </DialogTitle>
      
      <DialogContent 
        dividers 
        sx={{ 
          backgroundColor: 'background.default', 
          padding: 0, 
          display: 'flex', 
          flexDirection: 'column',
          height: '70vh'
        }}
      >
        {loading && (
          <Box display="flex" justifyContent="center" p={4}>
            <CircularProgress />
          </Box>
        )}

        {error && (
          <Box p={2}>
            <Typography color="error" variant="body2">
              Error: {error}
            </Typography>
          </Box>
        )}

        {yamlContent && !loading && (
          <AceEditor
            ref={editorRef}
            mode="yaml"
            value={yamlContent}
            readOnly={true}
            showPrintMargin={false}
            highlightActiveLine={false}
            width="100%"
            height="100%"
            setOptions={{
              useWorker: false,
              showLineNumbers: true,
              tabSize: 2,
              wrap: false,
              enableBasicAutocompletion: false,
              enableLiveAutocompletion: false,
              enableSnippets: false,
              showFoldWidgets: true,
            }}
            enableSearchBox={true}
          />
        )}
      </DialogContent>
    </Dialog>
  );
};

export default YamlViewer;
