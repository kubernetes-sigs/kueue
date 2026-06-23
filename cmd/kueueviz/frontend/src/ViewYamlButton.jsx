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
import { Button } from '@mui/material';
import YamlViewer from './YamlViewer';

const ViewYamlButton = ({ resourceType, resourceName, namespace = null }) => {
  const [yamlViewerOpen, setYamlViewerOpen] = useState(false);

  const handleViewYaml = (event) => {
    event.stopPropagation();
    setYamlViewerOpen(true);
  };

  const handleCloseYamlViewer = () => {
    setYamlViewerOpen(false);
  };

  return (
    <>
      <Button
        variant="text"
        size="small"
        color="primary"
        onClick={handleViewYaml}
        sx={{ textTransform: 'none' }}
      >
        View YAML
      </Button>
      
      <YamlViewer
        open={yamlViewerOpen}
        onClose={handleCloseYamlViewer}
        resourceType={resourceType}
        resourceName={resourceName}
        namespace={namespace}
      />
    </>
  );
};

export default ViewYamlButton;
