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

import { afterEach, beforeEach, describe, it, expect, vi } from 'vitest';
import '@testing-library/jest-dom/vitest';
import React from 'react';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import Login from './Login';
import { useAuth } from './AuthContext';

vi.mock('./AuthContext', () => ({
  useAuth: vi.fn(),
}));

const login = vi.fn();
const writeText = vi.fn();

const renderLogin = () =>
  render(
    <MemoryRouter initialEntries={['/login']}>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="/" element={<div>dashboard page</div>} />
      </Routes>
    </MemoryRouter>
  );

beforeEach(() => {
  vi.mocked(useAuth).mockReturnValue({ login });
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.useRealTimers();
});

describe('Login', () => {
  it('renders the instructions and a disabled sign-in button', () => {
    renderLogin();

    expect(screen.getByText('KueueViz Authentication')).toBeInTheDocument();
    expect(screen.getByText(/kubectl create token/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Sign In' })).toBeDisabled();
  });

  it('keeps the button disabled for a whitespace-only token', () => {
    renderLogin();

    fireEvent.change(screen.getByLabelText('Bearer Token'), { target: { value: '   ' } });

    expect(screen.getByRole('button', { name: 'Sign In' })).toBeDisabled();
  });

  it('does not log in when a whitespace-only form is submitted', () => {
    renderLogin();

    fireEvent.change(screen.getByLabelText('Bearer Token'), { target: { value: '   ' } });
    fireEvent.submit(screen.getByLabelText('Bearer Token').closest('form'));

    expect(login).not.toHaveBeenCalled();
    expect(screen.queryByText('dashboard page')).not.toBeInTheDocument();
  });

  it('masks the token input', () => {
    renderLogin();

    expect(screen.getByLabelText('Bearer Token')).toHaveAttribute('type', 'password');
  });

  it('logs in with the trimmed token and navigates to the dashboard', () => {
    renderLogin();

    fireEvent.change(screen.getByLabelText('Bearer Token'), { target: { value: '  my-token \n' } });
    fireEvent.click(screen.getByRole('button', { name: 'Sign In' }));

    expect(login).toHaveBeenCalledExactlyOnceWith('my-token');
    expect(screen.getByText('dashboard page')).toBeInTheDocument();
  });

  it('copies the token command to the clipboard and resets the confirmation', () => {
    vi.useFakeTimers();
    renderLogin();

    fireEvent.click(screen.getByRole('button', { name: 'Copy command' }));
    expect(writeText).toHaveBeenCalledExactlyOnceWith('kubectl create token <service-account> -n default --duration=168h');
    expect(screen.getByRole('button', { name: 'Copied!' })).toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(2000);
    });
    expect(screen.getByRole('button', { name: 'Copy command' })).toBeInTheDocument();
  });
});
