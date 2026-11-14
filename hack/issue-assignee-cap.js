/**
 * Enforces a maximum cap on open assigned issues per user.
 *
 * @param {object} params
 * @param {object} params.github - Authenticated Octokit client
 * @param {object} params.context - GitHub action context
 * @param {object} params.core - Actions core toolkit
 * @param {number} [params.maxCap=8] - Maximum allowed assigned open issues
 */
async function enforceCap({ github, context, core, maxCap = 8 }) {
  const assignee = context.payload?.assignee?.login;
  if (!assignee) {
    core.info('No assignee found in event payload.');
    return;
  }

  const issueNumber = context.payload.issue?.number;
  const owner = context.repo.owner;
  const repo = context.repo.repo;

  // Query all open issues assigned to the user
  const assignedItems = await github.paginate(github.rest.issues.listForRepo, {
    owner,
    repo,
    state: 'open',
    assignee,
    per_page: 100,
  });

  // Filter out Pull Requests (GitHub's issues API returns both issues and PRs)
  const openAssignedIssues = assignedItems.filter((item) => !item.pull_request);
  const openAssignedCount = openAssignedIssues.length;

  core.info(`User @${assignee} currently has ${openAssignedCount} open assigned issues (cap: ${maxCap}).`);

  if (openAssignedCount > maxCap) {
    core.info(`Cap exceeded (${openAssignedCount} > ${maxCap}). Unassigning @${assignee} from issue #${issueNumber}.`);

    await github.rest.issues.removeAssignees({
      owner,
      repo,
      issue_number: issueNumber,
      assignees: [assignee],
    });

    const otherCount = openAssignedCount - 1;
    const guideUrl = `https://github.com/${owner}/${repo}/blob/main/CONTRIBUTING.md#issue-assignment-policy`;
    const commentBody = `@${assignee}: Users are limited to a maximum of **${maxCap}** open assigned issues at a time (you currently have **${otherCount}** other open issues assigned).\n\nYou have been automatically unassigned from this issue so others can work on it. Please see our [contributor guide](${guideUrl}) and complete or \`/unassign\` one of your existing issues before assigning a new one.`;

    await github.rest.issues.createComment({
      owner,
      repo,
      issue_number: issueNumber,
      body: commentBody,
    });
  }
}

module.exports = enforceCap;
