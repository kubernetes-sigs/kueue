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

import React from 'react';
import { Link } from 'react-router-dom';
import { Table, TableBody, TableCell, TableContainer, TableHead, TableRow, Typography, Paper } from '@mui/material';
import './App.css';

const FlavorTable = ({ title, flavorData, linkToFlavor, showBorrowingColumn }) => (
  <>
    <Typography variant="h5" gutterBottom style={{ marginTop: '20px' }}>{title}</Typography>
    <TableContainer component={Paper} className="tableContainerWithBorder">
      <Table>
        <TableHead>
          <TableRow style={{ textDecoration: 'none', color: 'inherit', fontWeight: 'bold' }}>
            <TableCell>Flavor Name</TableCell>
            <TableCell>Resource</TableCell>
            <TableCell>Total</TableCell>
            {showBorrowingColumn && <TableCell>Borrowed</TableCell>}
          </TableRow>
        </TableHead>
        <TableBody>
          {flavorData?.map((flavor, index) => (
            <React.Fragment key={flavor.name}>
              {flavor.resources.map((resource, resIndex) => (
                <TableRow key={`${index}-${resIndex}`}>
                  {/* Display Flavor Name with rowSpan across all its resources */}
                  {resIndex === 0 && (
                    <TableCell rowSpan={flavor.resources.length}>
                      {linkToFlavor ? (
                        <Link to={`/resource-flavor/${flavor.name}`}>
                          {flavor.name}
                        </Link>
                      ) : (
                        flavor.name
                      )}
                    </TableCell>
                  )}
                  <TableCell>{resource.name}</TableCell>
                  <TableCell>{resource.total}</TableCell>
                  {showBorrowingColumn && <TableCell>{resource.borrowed || "N/A"}</TableCell>}
                </TableRow>
              ))}
            </React.Fragment>
          ))}
        </TableBody>
      </Table>
    </TableContainer>
  </>
);

export default FlavorTable;
