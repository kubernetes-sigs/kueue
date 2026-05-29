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

import { describe, it, expect } from 'vitest';
import { errorToString } from './ErrorMessage';

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
