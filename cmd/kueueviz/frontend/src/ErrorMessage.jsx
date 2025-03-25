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

import React from 'react';
import { Typography } from '@mui/material';
import './App.css';

const ErrorMessage = ({ error }) => {
  if (!error) return null;

  // Replace \n with <br />
  const formattedError = error.replace(/\n/g, '<br />');

  return (
    <div className="error-message">
      <Typography variant="h6" color="error" gutterBottom>
        Error
      </Typography>
      <Typography variant="body1" color="textSecondary" dangerouslySetInnerHTML={{ __html: formattedError }} />
    </div>
  );
};

export default ErrorMessage; 