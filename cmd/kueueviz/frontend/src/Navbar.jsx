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

import React, { useState } from 'react';
import { Link } from 'react-router-dom';
import { AppBar, Toolbar, Button, Box, IconButton, Menu, MenuItem } from '@mui/material';
import MenuIcon from '@mui/icons-material/Menu';
import { useAuth } from './AuthContext';
import './App.css';

const Navbar = () => {
  const { token, logout, authMode } = useAuth();
  const [anchorEl, setAnchorEl] = useState(null);

  const handleOpenMenu = (event) => {
    setAnchorEl(event.currentTarget);
  };

  const handleCloseMenu = () => {
    setAnchorEl(null);
  };

  const navItems = [
    { label: 'Dashboard', to: '/' },
    { label: 'Workloads', to: '/workloads' },
    { label: 'Local Queues', to: '/local-queues' },
    { label: 'Cluster Queues', to: '/cluster-queues' },
    { label: 'Cohorts', to: '/cohorts' },
    { label: 'Resource Flavors', to: '/resource-flavors' },
  ];

  return (
    <AppBar position="static">
      <Toolbar>
        <Link to="/" className="navbar-link">
          <img src="/kueueviz.png" className="navbar-logo" alt="KueueViz Logo" />
        </Link>

        {/* Mobile Navigation Menu */}
        <Box sx={{ display: { xs: 'flex', md: 'none' }, ml: 'auto' }}>
          <IconButton
            size="large"
            aria-label="menu"
            aria-controls="menu-appbar"
            aria-haspopup="true"
            onClick={handleOpenMenu}
            color="inherit"
          >
            <MenuIcon />
          </IconButton>
          <Menu
            id="menu-appbar"
            anchorEl={anchorEl}
            anchorOrigin={{
              vertical: 'bottom',
              horizontal: 'right',
            }}
            keepMounted
            transformOrigin={{
              vertical: 'top',
              horizontal: 'right',
            }}
            open={Boolean(anchorEl)}
            onClose={handleCloseMenu}
          >
            {navItems.map((item) => (
              <MenuItem
                key={item.label}
                component={Link}
                to={item.to}
                onClick={handleCloseMenu}
              >
                {item.label}
              </MenuItem>
            ))}
            {authMode !== 'Disabled' && token && (
              <MenuItem
                onClick={() => {
                  handleCloseMenu();
                  logout();
                }}
              >
                Logout
              </MenuItem>
            )}
          </Menu>
        </Box>

        {/* Desktop Navigation Buttons */}
        <Box sx={{ display: { xs: 'none', md: 'flex' }, gap: 1, ml: 2 }}>
          {navItems.map((item) => (
            <Button
              key={item.label}
              color="inherit"
              component={Link}
              to={item.to}
            >
              {item.label}
            </Button>
          ))}
        </Box>

        {/* Desktop Logout Button */}
        {authMode !== 'Disabled' && token && (
          <Box sx={{ display: { xs: 'none', md: 'flex' }, ml: 'auto' }}>
            <Button color="inherit" onClick={logout}>
              Logout
            </Button>
          </Box>
        )}
      </Toolbar>
    </AppBar>
  );
};

export default Navbar;


