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

import { afterEach, describe, it, expect, vi } from 'vitest';
import '@testing-library/jest-dom/vitest';
import React from 'react';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import Navbar from './Navbar';
import { useAuth } from './AuthContext';

vi.mock('./AuthContext', () => ({
  useAuth: vi.fn(),
}));

const logout = vi.fn();

const renderNavbar = (auth) => {
  vi.mocked(useAuth).mockReturnValue({ logout, ...auth });
  return render(
    <MemoryRouter>
      <Navbar />
    </MemoryRouter>
  );
};

// The desktop buttons and the mobile menu items are both in the DOM; the
// breakpoint is applied with CSS only. Return the links of the desktop bar.
const desktopLinks = () =>
  screen
    .getAllByRole('link', { hidden: true })
    .filter((link) => link.classList.contains('MuiButton-root'));

// The mobile menu is keepMounted, so MUI hides the rest of the page from the
// accessibility tree. Query its toggle explicitly as hidden.
const openMobileMenu = () => screen.getByRole('button', { name: 'menu', hidden: true });

const expectedNavItems = [
  ['Dashboard', '/'],
  ['Workloads', '/workloads'],
  ['Local Queues', '/local-queues'],
  ['Cluster Queues', '/cluster-queues'],
  ['Cohorts', '/cohorts'],
  ['Resource Flavors', '/resource-flavors'],
];

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('Navbar', () => {
  it('links to every top-level page from the desktop bar', () => {
    renderNavbar({ token: null, authMode: 'Disabled' });

    expect(desktopLinks().map((link) => [link.textContent, link.getAttribute('href')])).toEqual(expectedNavItems);
  });

  it('links to every top-level page from the mobile menu', () => {
    renderNavbar({ token: null, authMode: 'Disabled' });

    fireEvent.click(openMobileMenu());

    const menu = screen.getByRole('menu');
    const items = within(menu).getAllByRole('menuitem');
    expect(items.map((item) => [item.textContent, item.getAttribute('href')])).toEqual(expectedNavItems);
  });

  it('shows the logo linking to the dashboard', () => {
    renderNavbar({ token: null, authMode: 'Disabled' });

    const logo = screen.getByRole('img', { name: 'KueueViz Logo' });
    expect(logo.closest('a')).toHaveAttribute('href', '/');
  });

  it.each([
    { name: 'authentication is disabled', token: 'tok', authMode: 'Disabled' },
    { name: 'there is no token', token: null, authMode: 'Required' },
  ])('hides logout when $name', ({ token, authMode }) => {
    renderNavbar({ token, authMode });

    expect(screen.queryByText('Logout')).not.toBeInTheDocument();
  });

  it('logs out from the desktop bar', () => {
    renderNavbar({ token: 'tok', authMode: 'Required' });

    fireEvent.click(screen.getByRole('button', { name: 'Logout' }));

    expect(logout).toHaveBeenCalledTimes(1);
  });

  it('logs out from the mobile menu', () => {
    renderNavbar({ token: 'tok', authMode: 'Required' });

    fireEvent.click(openMobileMenu());
    fireEvent.click(screen.getByRole('menuitem', { name: 'Logout' }));

    expect(logout).toHaveBeenCalledTimes(1);
  });
});
