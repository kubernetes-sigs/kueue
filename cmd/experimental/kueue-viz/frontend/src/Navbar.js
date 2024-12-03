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


