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
import { act, cleanup, fireEvent, render, renderHook, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { AuthProvider, RequireAuth, useAuth, useAuthFetch } from './AuthContext';

vi.mock('./utils/urlHelper', () => ({
  getBackendHttpUrl: () => 'http://backend',
}));

const TOKEN_KEY = 'kueueviz_token';

const mockAuthStatus = (authMode) => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue({ json: () => Promise.resolve({ authMode }) })
  );
};

const LocationProbe = () => <div data-testid="location">{useLocation().pathname}</div>;

const wrapper = ({ children }) => (
  <MemoryRouter initialEntries={['/']}>
    <AuthProvider>{children}</AuthProvider>
    <LocationProbe />
  </MemoryRouter>
);

beforeEach(() => {
  sessionStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('useAuth', () => {
  it('throws outside of an AuthProvider', () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});

    try {
      expect(() => renderHook(() => useAuth())).toThrow('useAuth must be used within AuthProvider');
    } finally {
      consoleError.mockRestore();
    }
  });
});

describe('AuthProvider', () => {
  it('loads the auth mode from the backend', async () => {
    mockAuthStatus('Required');

    const { result } = renderHook(() => useAuth(), { wrapper });

    expect(result.current.authMode).toBeNull();
    await waitFor(() => expect(result.current.authMode).toBe('Required'));
    expect(fetch).toHaveBeenCalledWith('http://backend/auth/status');
  });

  it('falls back to Disabled when the auth status cannot be fetched', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('network down')));

    const { result } = renderHook(() => useAuth(), { wrapper });

    await waitFor(() => expect(result.current.authMode).toBe('Disabled'));
  });

  it('restores the token from the session storage', () => {
    mockAuthStatus('Required');
    sessionStorage.setItem(TOKEN_KEY, 'stored-token');

    const { result } = renderHook(() => useAuth(), { wrapper });

    expect(result.current.token).toBe('stored-token');
  });

  it('stores the token on login', () => {
    mockAuthStatus('Required');
    const { result } = renderHook(() => useAuth(), { wrapper });

    act(() => result.current.login('new-token'));

    expect(result.current.token).toBe('new-token');
    expect(sessionStorage.getItem(TOKEN_KEY)).toBe('new-token');
  });

  it('clears the token and navigates to the login page on logout', () => {
    mockAuthStatus('Required');
    sessionStorage.setItem(TOKEN_KEY, 'stored-token');
    const { result } = renderHook(() => useAuth(), { wrapper });

    act(() => result.current.logout());

    expect(result.current.token).toBeNull();
    expect(sessionStorage.getItem(TOKEN_KEY)).toBeNull();
    expect(screen.getByTestId('location')).toHaveTextContent('/login');
  });
});

describe('RequireAuth', () => {
  const renderProtected = () =>
    render(
      <MemoryRouter initialEntries={['/secret']}>
        <AuthProvider>
          <Routes>
            <Route path="/login" element={<div>login page</div>} />
            <Route path="/secret" element={<RequireAuth><div>secret page</div></RequireAuth>} />
          </Routes>
        </AuthProvider>
      </MemoryRouter>
    );

  it('renders nothing while the auth mode is unknown', () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise(() => {})));

    renderProtected();

    expect(screen.queryByText('secret page')).not.toBeInTheDocument();
    expect(screen.queryByText('login page')).not.toBeInTheDocument();
  });

  it('redirects to the login page when authentication is required and there is no token', async () => {
    mockAuthStatus('Required');

    renderProtected();

    expect(await screen.findByText('login page')).toBeInTheDocument();
  });

  it('renders the page when authentication is required and a token exists', async () => {
    mockAuthStatus('Required');
    sessionStorage.setItem(TOKEN_KEY, 'stored-token');

    renderProtected();

    expect(await screen.findByText('secret page')).toBeInTheDocument();
  });

  it('renders the page when authentication is disabled', async () => {
    mockAuthStatus('Disabled');

    renderProtected();

    expect(await screen.findByText('secret page')).toBeInTheDocument();
  });
});

describe('useAuthFetch', () => {
  const renderAuthFetch = async (token) => {
    mockAuthStatus('Required');
    if (token) sessionStorage.setItem(TOKEN_KEY, token);
    const { result } = renderHook(() => ({ authFetch: useAuthFetch(), auth: useAuth() }), { wrapper });
    await waitFor(() => expect(result.current.auth.authMode).toBe('Required'));
    fetch.mockClear();
    return result;
  };

  it('adds the bearer token and keeps the caller headers', async () => {
    const result = await renderAuthFetch('tok');
    fetch.mockResolvedValue({ status: 200 });

    await result.current.authFetch('http://backend/api/x', { headers: { Accept: 'text/yaml' } });

    expect(fetch).toHaveBeenCalledWith('http://backend/api/x', {
      headers: { Accept: 'text/yaml', Authorization: 'Bearer tok' },
    });
  });

  it('sends no Authorization header without a token', async () => {
    const result = await renderAuthFetch(null);
    fetch.mockResolvedValue({ status: 200 });

    await result.current.authFetch('http://backend/api/x');

    expect(fetch).toHaveBeenCalledWith('http://backend/api/x', { headers: {} });
  });

  it('logs out and rejects on a 401 response', async () => {
    const result = await renderAuthFetch('tok');
    fetch.mockResolvedValue({ status: 401 });

    await expect(result.current.authFetch('http://backend/api/x')).rejects.toThrow('Authentication required');

    await waitFor(() => expect(result.current.auth.token).toBeNull());
    expect(sessionStorage.getItem(TOKEN_KEY)).toBeNull();
    expect(screen.getByTestId('location')).toHaveTextContent('/login');
  });

  it('returns non-401 error responses to the caller', async () => {
    const result = await renderAuthFetch('tok');
    const response = { status: 500 };
    fetch.mockResolvedValue(response);

    await expect(result.current.authFetch('http://backend/api/x')).resolves.toBe(response);
    expect(result.current.auth.token).toBe('tok');
  });
});
