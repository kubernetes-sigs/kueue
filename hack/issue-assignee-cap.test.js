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

const { describe, it } = require('node:test');
const assert = require('node:assert');
const enforceCap = require('./issue-assignee-cap.js');

describe('enforceCap', () => {
  function createMockEnv({ assignee = 'test-user', issueNumber = 123, openIssuesCount = 0, includePr = false }) {
    const paginateCalls = [];
    const unassignCalls = [];
    const commentCalls = [];
    const loggedInfo = [];

    const listForRepoFn = () => {};

    const mockGithub = {
      paginate: async (method, params) => {
        paginateCalls.push({ method, params });
        const items = [];
        for (let i = 0; i < openIssuesCount; i++) {
          items.push({
            number: i + 1,
            pull_request: includePr && i === 0 ? {} : undefined,
          });
        }
        return items;
      },
      rest: {
        issues: {
          listForRepo: listForRepoFn,
          removeAssignees: async (args) => {
            unassignCalls.push(args);
          },
          createComment: async (args) => {
            commentCalls.push(args);
          },
        },
      },
    };

    const mockContext = {
      payload: {
        assignee: assignee ? { login: assignee } : undefined,
        issue: { number: issueNumber },
      },
      repo: {
        owner: 'kubernetes-sigs',
        repo: 'kueue',
      },
    };

    const mockCore = {
      info: (msg) => loggedInfo.push(msg),
    };

    return {
      mockGithub,
      mockContext,
      mockCore,
      listForRepoFn,
      paginateCalls,
      unassignCalls,
      commentCalls,
      loggedInfo,
    };
  }

  it('should exit early when no assignee is in payload', async () => {
    const { mockGithub, mockContext, mockCore, paginateCalls, unassignCalls } = createMockEnv({ assignee: null });
    await enforceCap({ github: mockGithub, context: mockContext, core: mockCore });
    assert.strictEqual(paginateCalls.length, 0);
    assert.strictEqual(unassignCalls.length, 0);
  });

  it('should allow assignment when open issues are at or below the cap (8)', async () => {
    const {
      mockGithub,
      mockContext,
      mockCore,
      listForRepoFn,
      paginateCalls,
      unassignCalls,
      commentCalls,
    } = createMockEnv({
      openIssuesCount: 8,
    });
    await enforceCap({ github: mockGithub, context: mockContext, core: mockCore });

    assert.strictEqual(paginateCalls.length, 1);
    assert.strictEqual(paginateCalls[0].method, listForRepoFn);
    assert.deepStrictEqual(paginateCalls[0].params, {
      owner: 'kubernetes-sigs',
      repo: 'kueue',
      state: 'open',
      assignee: 'test-user',
      per_page: 100,
    });
    assert.strictEqual(unassignCalls.length, 0);
    assert.strictEqual(commentCalls.length, 0);
  });

  it('should unassign and comment when open issues exceed the cap (9 > 8)', async () => {
    const {
      mockGithub,
      mockContext,
      mockCore,
      listForRepoFn,
      paginateCalls,
      unassignCalls,
      commentCalls,
    } = createMockEnv({
      openIssuesCount: 9,
      issueNumber: 42,
    });
    await enforceCap({ github: mockGithub, context: mockContext, core: mockCore });

    assert.strictEqual(paginateCalls.length, 1);
    assert.strictEqual(paginateCalls[0].method, listForRepoFn);
    assert.deepStrictEqual(paginateCalls[0].params, {
      owner: 'kubernetes-sigs',
      repo: 'kueue',
      state: 'open',
      assignee: 'test-user',
      per_page: 100,
    });

    assert.strictEqual(unassignCalls.length, 1);
    assert.deepStrictEqual(unassignCalls[0], {
      owner: 'kubernetes-sigs',
      repo: 'kueue',
      issue_number: 42,
      assignees: ['test-user'],
    });

    assert.strictEqual(commentCalls.length, 1);
    assert.strictEqual(commentCalls[0].issue_number, 42);
    assert.match(commentCalls[0].body, /maximum of \*\*8\*\* open assigned issues/);
    assert.match(commentCalls[0].body, /currently have \*\*8\*\* other open issues/);
    assert.match(commentCalls[0].body, /CONTRIBUTING\.md#issue-assignment-policy/);
  });

  it('should ignore pull requests when counting open assigned issues', async () => {
    const {
      mockGithub,
      mockContext,
      mockCore,
      listForRepoFn,
      paginateCalls,
      unassignCalls,
      commentCalls,
    } = createMockEnv({
      openIssuesCount: 9,
      includePr: true,
    });
    await enforceCap({ github: mockGithub, context: mockContext, core: mockCore });

    assert.strictEqual(paginateCalls.length, 1);
    assert.strictEqual(paginateCalls[0].method, listForRepoFn);
    assert.deepStrictEqual(paginateCalls[0].params, {
      owner: 'kubernetes-sigs',
      repo: 'kueue',
      state: 'open',
      assignee: 'test-user',
      per_page: 100,
    });
    assert.strictEqual(unassignCalls.length, 0);
    assert.strictEqual(commentCalls.length, 0);
  });
});
