describe('Kueue Dashboard', () => {
  beforeEach(() => {
    cy.visit('/')
  })


  it('should have the correct title', () => {
    cy.title().should('contain', 'Kueue Dashboard')
  })

  it('should navigate to resource-flavors and verify table content', () => {
    // Check for the link to resource-flavors
    cy.get('a[href="/resource-flavors"]').should('exist')
      .click()

    // Verify the table structure and content
    cy.get('table').should('exist')
    cy.get('th').contains('Name')
    cy.get('th').contains('Details')

    // Check for specific row values
    cy.get('td').contains('default-flavor')
    cy.get('td').contains('gpu')
    cy.get('td').contains('spot')
  })

  it('should navigate to cluster-queue and verify local queues', { defaultCommandTimeout: 15000 }, () => {
    cy.visit('/cluster-queues')
    // Find and click the link to /cluster-queue/agi-cluster-queue
    cy.get('a[href="/cluster-queue/agi-cluster-queue"]').should('exist')
      .click()

    // Verify the section and table
    cy.contains('Local Queues Using This Cluster Queue').should('exist')
    cy.get('th').contains('Queue Name')

    // Navigate to the first link in the table
    cy.get('table').find('a').first().click()
  })

  it('should navigate to cluster-queue unused-cluster-queue', { defaultCommandTimeout: 15000 }, () => {
    cy.visit('/cluster-queues')
    // Find and click the link to /cluster-queue/unused-cluster-queue
    cy.get('a[href="/cluster-queue/unused-cluster-queue"]').should('exist')
      .click()

    // Verify the section and table
    cy.contains('Local Queues Using This Cluster Queue').should('exist')
    cy.get('th').contains('Queue Name')
    // the table has one empty row
    cy.get('table').find('td').should('have.text', 'No local queues using this cluster queue')

  })


  it('should verify cohort link and navigate to cohorts page', { defaultCommandTimeout: 15000 }, () => {
    // Navigate to /cluster-queue/agi-cluster-queue
    cy.visit('/cluster-queue/agi-cluster-queue')

    // Verify the link to /cohort/ai-for-humanity-foundation
    cy.get('a[href="/cohort/ai-for-humanity-foundation"]').should('exist')

    // Navigate to /cohorts
    cy.get('a[href="/cohorts"]').should('exist')
      .click()

    // Verify the table and its content
    cy.get('table').should('exist')
    cy.get('th').contains('Cluster Queue Name')
    cy.get('td').contains('agi-cluster-queue')
    cy.get('td').contains('emergency-cluster-queue')
    cy.get('td').contains('llm-cluster-queue')
  })

  it('should display orphan cohort without any cluster queues', { defaultCommandTimeout: 15000 }, () => {
    // Navigate to cohorts page
    cy.visit('/cohorts')

    // Verify orphan cohort appears in the list (has no ClusterQueues)
    cy.get('table').should('exist')
    cy.get('td').contains('orphan-cohort-for-testing')

    // Navigate to the orphan cohort detail page
    cy.get('a[href="/cohort/orphan-cohort-for-testing"]').should('exist')
      .click()

    // Verify the cohort detail page loads correctly
    cy.contains('orphan-cohort-for-testing').should('exist')
  })

  it('should toggle between list and tree view on cohorts page', { defaultCommandTimeout: 15000 }, () => {
    cy.visit('/cohorts')
    cy.get('table').should('exist')
    cy.contains('button', 'Tree').click()
    cy.get('ul').should('exist')
    cy.contains(/\d+ child/).should('exist')
    cy.contains('button', 'List').click()
    cy.get('table').should('exist')
  })

  it('should verify the presence of all main links', () => {
    const links = [
      '/workloads',
      '/local-queues',
      '/cluster-queues',
      '/cohorts',
      '/resource-flavors'
    ];

    links.forEach(link => {
      cy.get(`a[href="${link}"]`).should('exist').click();
    });
  })

  it('should open and close YAML viewer modal on resource-flavors page', { defaultCommandTimeout: 15000 }, () => {
    cy.visit('/resource-flavors')

    cy.get('table').should('exist')

    cy.contains('button', 'View YAML').first().click()

    cy.get('[role="dialog"]').should('be.visible')

    cy.get('[role="dialog"]').within(() => {
      cy.get('h6').should('exist')
      cy.get('button').find('svg').should('exist')
    })

    cy.get('[role="dialog"]').within(() => {
      cy.get('button').find('svg').click()
    })

    cy.get('[role="dialog"]').should('not.exist')
  })

  // --- Resource Usage E2E Tests ---

  it('should display resource usage columns on cluster-queues list', { defaultCommandTimeout: 15000 }, () => {
    cy.visit('/cluster-queues')

    cy.get('table').should('exist')

    // Resource name columns are dynamically discovered; verify cpu and memory headers appear
    cy.get('th').contains('cpu')
    cy.get('th').contains('memory')

    // agi-cluster-queue row should contain a usage bar
    cy.get('a[href="/cluster-queue/agi-cluster-queue"]')
      .closest('tr')
      .within(() => {
        cy.get('[data-testid="usage-bar"]').should('have.length.greaterThan', 0)
      })

    // unused-cluster-queue has no resource groups, so usage cells should show '-'
    cy.get('a[href="/cluster-queue/unused-cluster-queue"]')
      .closest('tr')
      .within(() => {
        cy.contains('td', '-').should('exist')
      })
  })

  it('should display resource quotas and usage table on cluster-queue detail', { defaultCommandTimeout: 15000 }, () => {
    cy.visit('/cluster-queue/agi-cluster-queue')

    // Verify the quota/usage section exists
    cy.contains('Resource Quotas & Usage').should('exist')

    // Verify table column headers
    cy.get('th').contains('Flavor')
    cy.get('th').contains('Resource')
    cy.get('th').contains('Nominal Quota')
    cy.get('th').contains('Usage')
    cy.get('th').contains('Borrowed')
    cy.get('th').contains('Borrowing Limit')
    cy.get('th').contains('Lending Limit')
    cy.get('th').contains('Utilization')

    // Verify specific row data: default-flavor, cpu with 300m quota
    cy.get('a[href="/resource-flavor/default-flavor"]').should('exist')
    cy.contains('td', 'cpu').should('exist')
    cy.contains('td', '300m').should('exist')

    // Verify memory row exists
    cy.contains('td', 'memory').should('exist')
    cy.contains('td', '300Mi').should('exist')
  })

  it('should toggle show reservation column on cluster-queue detail', { defaultCommandTimeout: 15000 }, () => {
    cy.visit('/cluster-queue/agi-cluster-queue')

    cy.contains('Resource Quotas & Usage').should('exist')

    // Reservation column should not be visible in the Resource Quotas table initially
    cy.get('[data-testid="resource-quotas-section"]').find('table').within(() => {
      cy.get('th').contains('Reservation').should('not.exist')
    })

    // Toggle the Show Reservation switch
    cy.contains('Show Reservation').click()

    // Reservation column should now be visible in the Resource Quotas table
    cy.get('[data-testid="resource-quotas-section"]').find('table').within(() => {
      cy.get('th').contains('Reservation').should('exist')
    })
  })

  it('should display no resource groups message for unused cluster queue', { defaultCommandTimeout: 15000 }, () => {
    cy.visit('/cluster-queue/unused-cluster-queue')

    cy.contains('No resource groups defined for this cluster queue.').should('exist')
  })

  it('should display resource usage columns on local-queues list', { defaultCommandTimeout: 15000 }, () => {
    cy.visit('/local-queues')

    cy.get('table').should('exist')

    // Resource name columns should appear as headers
    cy.get('th').contains('cpu')
    cy.get('th').contains('memory')

    // agi-model-queue row should show its cluster queue and resource usage values
    cy.get('a[href="/local-queue/default/agi-model-queue"]')
      .closest('tr')
      .within(() => {
        cy.get('a[href="/cluster-queue/agi-cluster-queue"]').should('exist')
      })
  })

  it('should display flavor usage tables on local-queue detail', { defaultCommandTimeout: 15000 }, () => {
    cy.visit('/local-queue/default/agi-model-queue')

    // Verify the FlavorTable sections exist
    cy.contains('Flavor Usage').should('exist')
    cy.contains('Flavors Reservation').should('exist')

    // Verify FlavorTable column headers
    cy.get('th').contains('Flavor Name')
    cy.get('th').contains('Resource')
    cy.get('th').contains('Total')

    // Verify actual data: flavor name and resource types from agi-cluster-queue
    cy.get('a[href="/resource-flavor/default-flavor"]').should('exist')
    cy.contains('td', 'cpu').should('exist')
    cy.contains('td', 'memory').should('exist')

    // Verify Total column cells are populated (not empty)
    cy.contains('Flavor Usage').next().find('table').within(() => {
      cy.get('tbody tr').should('have.length.greaterThan', 0)
      cy.get('tbody tr').first().find('td').last().invoke('text').should('not.be.empty')
    })
  })

  it('should display cohort resource utilization on cohort detail', { defaultCommandTimeout: 15000 }, () => {
    cy.visit('/cohort/ai-for-humanity-foundation')

    // Verify the aggregate utilization section
    cy.contains('Cohort Resource Utilization').should('exist')

    // Verify aggregate resource cards show cpu and memory with correct quota totals
    // Quotas from fixtures: agi(300m) + emergency(1) + llm(300m) = 1.6 cpu
    cy.contains('/ 1.6').should('exist')
    // Memory: agi(300Mi) + emergency(1Gi) + llm(300Mi) ≈ 1.6Gi
    cy.contains('/ 1.6Gi').should('exist')
    cy.get('[data-testid="usage-bar"]').should('have.length.greaterThan', 0)

    // Verify per-queue usage breakdown section
    cy.contains('Per-Queue Resource Usage').should('exist')

    // Verify the per-queue table contains all member cluster queues
    cy.get('a[href="/cluster-queue/agi-cluster-queue"]').should('exist')
    cy.get('a[href="/cluster-queue/emergency-cluster-queue"]').should('exist')
    cy.get('a[href="/cluster-queue/llm-cluster-queue"]').should('exist')

    // Verify per-queue table headers include resource columns
    cy.contains('h5', 'Per-Queue Resource Usage').next().find('table').within(() => {
      cy.get('th').contains('Queue')
      cy.get('th').contains('cpu')
      cy.get('th').contains('memory')
    })
  })

  it('should not display resource utilization for orphan cohort', { defaultCommandTimeout: 15000 }, () => {
    cy.visit('/cohort/orphan-cohort-for-testing')

    cy.contains('orphan-cohort-for-testing').should('exist')

    // Orphan cohort has no cluster queues, so resource utilization sections should not appear
    cy.contains('Cohort Resource Utilization').should('not.exist')
    cy.contains('Per-Queue Resource Usage').should('not.exist')
  })

  // Add more test cases here as needed
})