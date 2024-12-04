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
import ClusterQueues from './ClusterQueues';
import CohortDetail from './CohortDetail';
import Cohorts from './Cohorts';
import Dashboard from './Dashboard';
import LocalQueueDetail from './LocalQueueDetail';
import LocalQueues from './LocalQueues';
import Navbar from './Navbar';
import ResourceFlavorDetail from './ResourceFlavorDetail';
import ResourceFlavors from './ResourceFlavors';
import WorkloadDetail from './WorkloadDetail';
import Workloads from './Workloads';
import ClusterQueueDetail from './ClusterQueueDetail';

const App = () => {
  return (
    <Router>
      <Navbar />
      <Routes>
        <Route path="/" element={<Dashboard />} />
        <Route path="/workloads" element={<Workloads/>} />
        <Route path="/local-queues" element={<LocalQueues />} />
        <Route path="/cluster-queues" element={<ClusterQueues />} />
        <Route path="/resource-flavors" element={<ResourceFlavors />} />
        <Route path="/cohorts" element={<Cohorts />} />


        <Route path="/workload/:namespace/:workloadName" element={<WorkloadDetail />} />
        <Route path="/local-queue/:namespace/:queueName" element={<LocalQueueDetail />} />
        <Route path="/cluster-queue/:clusterQueueName" element={<ClusterQueueDetail />} />
        <Route path="/resource-flavor/:flavorName" element={<ResourceFlavorDetail />} />
        <Route path="/cohort/:cohortName" element={<CohortDetail />} />

      </Routes>
    </Router>
  );
};

export default App;
