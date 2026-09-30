/** @type {import('semantic-release').GlobalConfig} */
module.exports = {
  branches: ['master'],
  tagFormat: 'v${version}',
  plugins: [
    [
      '@semantic-release/commit-analyzer',
      {
        // Keep semantic versioning correct when a custom rule also matches.
        releaseRules: [
          { breaking: true, release: 'major' },
          { type: 'build', release: 'patch' },
          { type: 'refactor', release: 'patch' },
        ],
        preset: 'conventionalcommits',
      },
    ],
    [
      '@semantic-release/release-notes-generator',
      {
        preset: 'conventionalcommits',
        presetConfig: {
          // build and refactor trigger releases above, so they must also be
          // visible in the generated release notes and changelog.
          types: [
            { type: 'build', section: 'Build System', hidden: false },
            { type: 'refactor', section: 'Code Refactoring', hidden: false },
          ],
        },
      },
    ],
    '@semantic-release/changelog',
    [
      '@semantic-release/git',
      {
        assets: ['CHANGELOG.md'],
        message: 'chore(release): ${nextRelease.version} [skip ci]\n\n${nextRelease.notes}',
      },
    ],
    [
      '@semantic-release/github',
      {
        failComment: false,
        failTitle: false,
        labels: false,
        releasedLabels: false,
        successComment: false,
      },
    ],
  ],
};
