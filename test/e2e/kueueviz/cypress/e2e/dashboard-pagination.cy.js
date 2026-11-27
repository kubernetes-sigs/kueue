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

// Backend pagination and informer updates are covered by handler tests.
// This fixture exercises pagination controls in the rendered dashboard.
describe('Dashboard workload pagination', () => {
  it('pages workloads, preserves totals, and resets on namespace and size changes', () => {
    cy.intercept('GET', '**/auth/status', { authMode: 'Disabled' });
    cy.visit('/', {
      onBeforeLoad(win) {
        const NativeWebSocket = win.WebSocket;
        const workloads = Array.from({ length: 61 }, (_, i) => ({
          metadata: { namespace: i < 31 ? 'ns-a' : 'ns-b', name: `wl-${String(i).padStart(3, '0')}`, uid: `uid-${i}` },
          spec: { queueName: 'lq', podSets: [{ count: 1 }] },
          status: { conditions: i < 10 ? [{ type: 'Finished', status: 'True' }] : [] },
          pods: [],
        }));
        class DashboardWebSocket {
          static CONNECTING = NativeWebSocket.CONNECTING;
          constructor(url, protocols) {
            const parsed = new URL(url);
            if (!['/ws/namespaces', '/ws/workloads/dashboard'].includes(parsed.pathname)) return new NativeWebSocket(url, protocols);
            this.readyState = NativeWebSocket.CONNECTING;
            this.timer = win.setTimeout(() => {
              this.readyState = NativeWebSocket.OPEN;
              this.onopen?.();
              if (parsed.pathname === '/ws/namespaces') {
                this.onmessage?.({ data: JSON.stringify({ namespaces: ['ns-a', 'ns-b'] }) });
                return;
              }
              const filtered = workloads.filter(w => !parsed.searchParams.has('namespace') || w.metadata.namespace === parsed.searchParams.get('namespace'));
              const pageSize = Number(parsed.searchParams.get('pageSize'));
              const page = Number(parsed.searchParams.get('page'));
              this.onmessage?.({ data: JSON.stringify({ queues: [], clusterQueues: [], workloads: {
                items: filtered.slice(page * pageSize, (page + 1) * pageSize),
                workloads_by_uid: {},
                pagination: { page, pageSize, totalCount: filtered.length },
                finishedCount: filtered.filter(w => w.status.conditions.length > 0).length,
              } }) });
            }, 0);
          }
          close() { win.clearTimeout(this.timer); this.readyState = NativeWebSocket.CLOSED; }
        }
        win.WebSocket = DashboardWebSocket;
      },
    });
    cy.contains('Total Workloads').next().should('have.text', '61');
    cy.contains('Completed Workloads').next().should('have.text', '10');
    cy.get('a[href^="/workload/"]').should('have.length', 25);
    cy.contains('1–25 of 61').should('be.visible');
    cy.get('button[aria-label="Go to next page"]').click();
    cy.contains('26–50 of 61').should('be.visible');
    cy.get('a[href="/workload/ns-a/wl-025"]').should('be.visible');
    cy.contains('Total Workloads').next().should('have.text', '61');
    cy.get('[role="combobox"][aria-label="Filter by namespace"]').click();
    cy.get('[role="option"]').contains('ns-a').click();
    cy.contains('1–25 of 31').should('be.visible');
    cy.contains('Total Workloads').next().should('have.text', '31');
    cy.get('[role="combobox"]').last().click();
    cy.get('[role="option"]').contains('50').click();
    cy.contains('1–31 of 31').should('be.visible');
    cy.get('a[href^="/workload/"]').should('have.length', 31);
    cy.get('button[aria-label="Go to next page"]').should('be.disabled');
  });
});
