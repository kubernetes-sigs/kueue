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