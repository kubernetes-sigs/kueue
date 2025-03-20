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
import { AppBar, Toolbar, Button } from '@mui/material';
import './App.css';

const Navbar = () => {
  return (
    <AppBar position="static">
      <Toolbar>
        <Link to="/" className="navbar-link"><img src="/kueue-viz.png" className="navbar-logo"/></Link>
        <Button color="inherit" component={Link} to="/">Dashboard</Button>
        <Button color="inherit" component={Link} to="/workloads">Workloads</Button>
        <Button color="inherit" component={Link} to="/local-queues">Local Queues</Button>
        <Button color="inherit" component={Link} to="/cluster-queues">Cluster Queues</Button>
        <Button color="inherit" component={Link} to="/cohorts">Cohorts</Button>
        <Button color="inherit" component={Link} to="/resource-flavors">Resource Flavors</Button>
      </Toolbar>
    </AppBar>
  );
};

export default Navbar;


