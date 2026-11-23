// @vitest-environment jsdom
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

import { afterEach, describe, it, expect } from 'vitest';
import '@testing-library/jest-dom/vitest';
import React from 'react';
import { cleanup, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import FlavorTable from './FlavorTable';

afterEach(cleanup);

const flavorData = [
  {
    name: 'on-demand',
    resources: [
      { name: 'cpu', total: '4', borrowed: '1' },
      { name: 'memory', total: '8Gi' },
    ],
  },
  { name: 'spot', resources: [{ name: 'cpu', total: '2', borrowed: '0' }] },
];

const renderTable = (props) =>
  render(
    <MemoryRouter>
      <FlavorTable title="Flavor Usage" flavorData={flavorData} {...props} />
    </MemoryRouter>
  );

describe('FlavorTable', () => {
  it('renders the title and the base column headers', () => {
    renderTable();

    expect(screen.getByRole('heading', { name: 'Flavor Usage' })).toBeInTheDocument();
    ['Flavor Name', 'Resource', 'Total'].forEach((header) => {
      expect(screen.getByRole('columnheader', { name: header })).toBeInTheDocument();
    });
    expect(screen.queryByRole('columnheader', { name: 'Borrowed' })).not.toBeInTheDocument();
  });

  it('renders one row per resource', () => {
    renderTable();

    // 1 header row + 3 resource rows.
    expect(screen.getAllByRole('row')).toHaveLength(4);
    expect(screen.getAllByText('cpu')).toHaveLength(2);
    expect(screen.getByText('8Gi')).toBeInTheDocument();
  });

  it('shows the flavor name once per flavor, spanning all of its resources', () => {
    renderTable();

    const flavorCell = screen.getByText('on-demand').closest('td');
    expect(flavorCell).toHaveAttribute('rowspan', '2');
    expect(screen.getAllByText('on-demand')).toHaveLength(1);
  });

  it('renders flavor names as plain text unless linkToFlavor is set', () => {
    renderTable();

    expect(screen.queryByRole('link')).not.toBeInTheDocument();
  });

  it('links flavor names to the resource flavor page when linkToFlavor is set', () => {
    renderTable({ linkToFlavor: true });

    expect(screen.getByRole('link', { name: 'on-demand' })).toHaveAttribute('href', '/resource-flavor/on-demand');
    expect(screen.getByRole('link', { name: 'spot' })).toHaveAttribute('href', '/resource-flavor/spot');
  });

  it('shows the borrowed column, with N/A for missing or empty values', () => {
    renderTable({ showBorrowingColumn: true });

    expect(screen.getByRole('columnheader', { name: 'Borrowed' })).toBeInTheDocument();
    const memoryRow = screen.getByText('memory').closest('tr');
    expect(within(memoryRow).getByText('N/A')).toBeInTheDocument();
    const spotRow = screen.getByText('spot').closest('tr');
    // A borrowed value of "0" is a string and therefore truthy.
    expect(within(spotRow).getByText('0')).toBeInTheDocument();
  });

  it.each([undefined, [], [{ name: 'empty-flavor' }]])('renders only the header for flavorData %j', (data) => {
    renderTable({ flavorData: data });

    expect(screen.getAllByRole('row')).toHaveLength(1);
  });
});
