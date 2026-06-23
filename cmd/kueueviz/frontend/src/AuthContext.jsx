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

import React, { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';
import { Navigate, useNavigate } from 'react-router-dom';
import { getBackendHttpUrl } from './utils/urlHelper';

const AuthContext = createContext(null);

export const AuthProvider = ({ children }) => {
  const [token, setToken] = useState(() => sessionStorage.getItem('kueueviz_token'));
  const [authMode, setAuthMode] = useState(null);
  const navigate = useNavigate();

  useEffect(() => {
    fetch(`${getBackendHttpUrl()}/auth/status`)
      .then(statusResp => statusResp.json())
      .then(status => setAuthMode(status.authMode))
      .catch(() => setAuthMode('Disabled'));
  }, []);

  const login = useCallback((newToken) => {
    sessionStorage.setItem('kueueviz_token', newToken);
    setToken(newToken);
  }, []);

  const logout = useCallback(() => {
    sessionStorage.removeItem('kueueviz_token');
    setToken(null);
    navigate('/login');
  }, [navigate]);

  const value = useMemo(
    () => ({ token, login, logout, authMode }),
    [token, login, logout, authMode],
  );

  return (
    <AuthContext.Provider value={value}>
      {children}
    </AuthContext.Provider>
  );
};

export const useAuth = () => {
  const ctx = useContext(AuthContext);
  if (!ctx) {
    throw new Error('useAuth must be used within AuthProvider');
  }
  return ctx;
};

export const RequireAuth = ({ children }) => {
  const { token, authMode } = useAuth();
  if (authMode === null) return null;
  if (authMode !== 'Disabled' && !token) return <Navigate to="/login" replace />;
  return children;
};

export const useAuthFetch = () => {
  const { token, logout } = useAuth();

  return useCallback(async (url, options = {}) => {
    const headers = { ...options.headers };
    if (token) {
      headers['Authorization'] = `Bearer ${token}`;
    }

    const resp = await fetch(url, { ...options, headers });

    if (resp.status === 401) {
      logout();
      throw new Error('Authentication required');
    }

    return resp;
  }, [token, logout]);
};
