// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest';
import '@testing-library/jest-dom/vitest';
import React from 'react';
import { render, screen } from '@testing-library/react';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import CohortDetail from './CohortDetail';
import * as useWebSocketModule from './useWebSocket';

// Mock the useWebSocket hook
vi.mock('./useWebSocket', () => ({
  default: vi.fn(),
}));

describe('CohortDetail', () => {
  const renderComponent = () => {
    return render(
      <MemoryRouter initialEntries={['/cohort/test-cohort']}>
        <Routes>
          <Route path="/cohort/:cohortName" element={<CohortDetail />} />
        </Routes>
      </MemoryRouter>
    );
  };

  it('renders loading state initially', () => {
    vi.mocked(useWebSocketModule.default).mockReturnValue({
      data: null,
      error: null,
    });

    renderComponent();
    expect(screen.getByText('Loading...')).toBeInTheDocument();
  });

  it('renders ErrorMessage when websocket returns an error', () => {
    vi.mocked(useWebSocketModule.default).mockReturnValue({
      data: null,
      error: 'WebSocket connection failed',
    });

    renderComponent();
    // ErrorMessage component simply renders the error string (based on our mock or actual implementation)
    expect(screen.getByText(/WebSocket connection failed/)).toBeInTheDocument();
  });

  it('renders ErrorMessage when API payload contains an error object (regression test for #12571)', () => {
    // This simulates the case where the backend returns {"error": "..."} instead of the Cohort object.
    vi.mocked(useWebSocketModule.default).mockReturnValue({
      data: { error: 'error fetching cohort' },
      error: null,
    });

    renderComponent();
    expect(screen.getByText(/error fetching cohort/)).toBeInTheDocument();
  });

  it('renders cohort details when data is valid', () => {
    vi.mocked(useWebSocketModule.default).mockReturnValue({
      data: {
        clusterQueues: [
          { name: 'cq-1', spec: {} },
          { name: 'cq-2', spec: {} },
        ],
      },
      error: null,
    });

    renderComponent();
    expect(screen.getByText('Cohort Detail: test-cohort')).toBeInTheDocument();
    expect(screen.getByText('Number of Cluster Queues:')).toBeInTheDocument();
    expect(screen.getByText('2')).toBeInTheDocument();
  });
});
