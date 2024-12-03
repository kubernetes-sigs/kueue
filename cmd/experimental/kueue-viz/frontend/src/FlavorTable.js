import React from 'react';
import { Link } from 'react-router-dom';
import { Table, TableBody, TableCell, TableContainer, TableHead, TableRow, Typography, Paper } from '@mui/material';
import './App.css';

const FlavorTable = ({ title, flavorData, linkToFlavor, showBorrowingColumn }) => (
  <>
    <Typography variant="h5" gutterBottom style={{ marginTop: '20px' }}>{title}</Typography>
    <TableContainer component={Paper}>
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
