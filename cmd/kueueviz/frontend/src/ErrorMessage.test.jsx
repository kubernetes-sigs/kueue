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

import { afterEach, describe, it, expect } from 'vitest';
import '@testing-library/jest-dom/vitest';
import React from 'react';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import ErrorMessage, { errorToString } from './ErrorMessage';

afterEach(cleanup);

// ---------------------------------------------------------------------------
// errorToString
// ---------------------------------------------------------------------------
describe('errorToString', () => {
  it('returns plain strings unchanged', () => {
    expect(errorToString('something went wrong')).toBe('something went wrong');
  });

  it('preserves multi-line strings', () => {
    const input = 'line one\nline two\nline three';
    expect(errorToString(input)).toBe(input);
  });

  it('returns empty string unchanged', () => {
    expect(errorToString('')).toBe('');
  });

  it('extracts message from Error objects', () => {
    const err = new Error('connection refused');
    expect(errorToString(err)).toBe('connection refused');
  });

  it('extracts message from TypeError', () => {
    const err = new TypeError('cannot read property');
    expect(errorToString(err)).toBe('cannot read property');
  });

  it('extracts message from objects with a message property', () => {
    const obj = { message: 'custom error message', code: 500 };
    expect(errorToString(obj)).toBe('custom error message');
  });

  it('JSON-stringifies objects without a message property', () => {
    const obj = { code: 404, reason: 'not found' };
    expect(errorToString(obj)).toBe(JSON.stringify(obj));
  });

  it('JSON-stringifies objects with an empty message', () => {
    const obj = { message: '', code: 500 };
    expect(errorToString(obj)).toBe(JSON.stringify(obj));
  });

  it('handles WebSocket-like event objects', () => {
    // WebSocket error events are plain objects with no useful message
    const event = { isTrusted: true, type: 'error' };
    expect(errorToString(event)).toBe(JSON.stringify(event));
  });

  it('handles number input', () => {
    expect(errorToString(42)).toBe('42');
  });

  it('handles boolean input', () => {
    expect(errorToString(true)).toBe('true');
  });

  it('handles null input', () => {
    expect(errorToString(null)).toBe('null');
  });

  it('handles undefined input', () => {
    expect(errorToString(undefined)).toBe(undefined);
  });
});

// ---------------------------------------------------------------------------
// <ErrorMessage />
// ---------------------------------------------------------------------------
describe('ErrorMessage', () => {
  it.each([null, undefined, '', 0, false])('renders nothing for the falsy error %j', (error) => {
    const { container } = render(<ErrorMessage error={error} />);

    expect(container).toBeEmptyDOMElement();
  });

  it('renders the heading and a single-line message without a details toggle', () => {
    render(<ErrorMessage error="Failed to connect to WebSocket." />);

    expect(screen.getByRole('heading', { name: 'Error' })).toBeInTheDocument();
    expect(screen.getByText('Failed to connect to WebSocket.')).toBeInTheDocument();
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });

  it('renders the message of an Error object', () => {
    render(<ErrorMessage error={new Error('boom')} />);

    expect(screen.getByText('boom')).toBeInTheDocument();
  });

  it('renders an arbitrary object as JSON', () => {
    render(<ErrorMessage error={{ code: 500 }} />);

    expect(screen.getByText('{"code":500}')).toBeInTheDocument();
  });

  it('shows only the first line as the summary and hides the details by default', () => {
    render(<ErrorMessage error={'summary line\ndetail one\ndetail two'} />);

    expect(screen.getByText('summary line')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Show Details/ })).toBeInTheDocument();
    expect(screen.getByText(/detail one/)).not.toBeVisible();
  });

  it('expands and collapses the details', () => {
    render(<ErrorMessage error={'summary line\ndetail one\ndetail two'} />);

    fireEvent.click(screen.getByRole('button', { name: /Show Details/ }));
    const details = screen.getByText(/detail one/);
    expect(details).toBeVisible();
    expect(details.textContent).toBe('detail one\ndetail two');
    expect(screen.getByRole('button', { name: /Hide Details/ })).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: /Hide Details/ }));
    expect(screen.getByRole('button', { name: /Show Details/ })).toBeInTheDocument();
  });
});
