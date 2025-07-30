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

  // Add more test cases here as needed
}) 