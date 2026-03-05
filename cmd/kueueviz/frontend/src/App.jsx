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
import { Route, BrowserRouter as Router, Routes } from 'react-router-dom';
import { AuthProvider, RequireAuth } from './AuthContext';
import ClusterQueues from './ClusterQueues';
import CohortDetail from './CohortDetail';
import Cohorts from './Cohorts';
import Dashboard from './Dashboard';
import LocalQueueDetail from './LocalQueueDetail';
import LocalQueues from './LocalQueues';
import Login from './Login';
import Navbar from './Navbar';
import ResourceFlavorDetail from './ResourceFlavorDetail';
import ResourceFlavors from './ResourceFlavors';
import WorkloadDetail from './WorkloadDetail';
import Workloads from './Workloads';
import ClusterQueueDetail from './ClusterQueueDetail';

const App = () => {
  return (
    <Router>
      <AuthProvider>
        <Navbar />
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route path="/" element={<RequireAuth><Dashboard /></RequireAuth>} />
          <Route path="/workloads" element={<RequireAuth><Workloads/></RequireAuth>} />
          <Route path="/local-queues" element={<RequireAuth><LocalQueues /></RequireAuth>} />
          <Route path="/cluster-queues" element={<RequireAuth><ClusterQueues /></RequireAuth>} />
          <Route path="/resource-flavors" element={<RequireAuth><ResourceFlavors /></RequireAuth>} />
          <Route path="/cohorts" element={<RequireAuth><Cohorts /></RequireAuth>} />

          <Route path="/workload/:namespace/:workloadName" element={<RequireAuth><WorkloadDetail /></RequireAuth>} />
          <Route path="/local-queue/:namespace/:queueName" element={<RequireAuth><LocalQueueDetail /></RequireAuth>} />
          <Route path="/cluster-queue/:clusterQueueName" element={<RequireAuth><ClusterQueueDetail /></RequireAuth>} />
          <Route path="/resource-flavor/:flavorName" element={<RequireAuth><ResourceFlavorDetail /></RequireAuth>} />
          <Route path="/cohort/:cohortName" element={<RequireAuth><CohortDetail /></RequireAuth>} />
        </Routes>
      </AuthProvider>
    </Router>
  );
};

export default App;
