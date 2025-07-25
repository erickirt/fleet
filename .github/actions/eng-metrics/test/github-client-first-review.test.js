import { jest } from '@jest/globals';
import GitHubClient from '../src/github-client.js';

// Mock the logger to avoid console output during tests
jest.mock('../src/logger.js', () => ({
  info: jest.fn(),
  warn: jest.fn(),
  error: jest.fn(),
  debug: jest.fn(),
  default: {
    info: jest.fn(),
    warn: jest.fn(),
    error: jest.fn(),
    debug: jest.fn(),
  }
}));

describe('GitHubClient - Time to first review (pickup time)', () => {
  let githubClient;

  beforeEach(() => {
    // Create a new instance of GitHubClient for each test
    githubClient = new GitHubClient('fake-token');

    // Mock the Octokit instance
    githubClient.octokit = {
      rest: {
        pulls: {
          list: jest.fn(),
          listReviews: jest.fn(),
        },
        issues: {
          listEventsForTimeline: jest.fn(),
        }
      }
    };
  });

  describe('calculatePickupTime', () => {
    // Table-driven test cases for calculatePickupTime
    const testCases = [
      {
        name: 'PR created as non-draft with one review',
        pr: {
          number: 123,
          html_url: 'https://github.com/owner/repo/pull/123',
          draft: false,
          created_at: '2023-05-10T10:00:00Z',
          user: { login: 'author' },
          base: {
            ref: 'main',
            repo: {
              name: 'repo',
              owner: { login: 'owner' }
            }
          }
        },
        timelineEvents: [],
        reviewEvents: [
          { submitted_at: '2023-05-10T11:30:00Z' }
        ],
        expected: {
          metricType: 'time_to_first_review',
          repository: 'owner/repo',
          prNumber: 123,
          prUrl: 'https://github.com/owner/repo/pull/123',
          prCreator: 'author',
          targetBranch: 'main',
          readyTime: new Date('2023-05-10T10:00:00Z'),
          firstReviewTime: new Date('2023-05-10T11:30:00Z'),
          reviewDate: '2023-05-10',
          pickupTimeSeconds: 5400, // 1.5 hours = 5400 seconds
          readyEventType: 'PR creation (not draft)'
        }
      },
      {
        name: 'PR created as draft, then marked as ready for review, then reviewed',
        pr: {
          number: 124,
          html_url: 'https://github.com/owner/repo/pull/124',
          draft: true,
          created_at: '2023-05-11T09:00:00Z',
          user: { login: 'author' },
          base: {
            ref: 'main',
            repo: {
              name: 'repo',
              owner: { login: 'owner' }
            }
          }
        },
        timelineEvents: [
          {
            event: 'ready_for_review',
            created_at: '2023-05-11T10:00:00Z'
          }
        ],
        reviewEvents: [
          { submitted_at: '2023-05-11T11:00:00Z' }
        ],
        expected: {
          metricType: 'time_to_first_review',
          repository: 'owner/repo',
          prNumber: 124,
          prUrl: 'https://github.com/owner/repo/pull/124',
          prCreator: 'author',
          targetBranch: 'main',
          readyTime: new Date('2023-05-11T10:00:00Z'),
          firstReviewTime: new Date('2023-05-11T11:00:00Z'),
          reviewDate: '2023-05-11',
          pickupTimeSeconds: 3600, // 1 hour = 3600 seconds
          readyEventType: 'ready_for_review event'
        }
      },
      {
        name: 'PR with multiple ready_for_review events - should use the latest one',
        pr: {
          number: 125,
          html_url: 'https://github.com/owner/repo/pull/125',
          draft: true,
          created_at: '2023-05-12T09:00:00Z',
          user: { login: 'author' },
          base: {
            ref: 'main',
            repo: {
              name: 'repo',
              owner: { login: 'owner' }
            }
          }
        },
        timelineEvents: [
          {
            event: 'ready_for_review',
            created_at: '2023-05-12T10:00:00Z'
          },
          {
            event: 'convert_to_draft',
            created_at: '2023-05-12T11:00:00Z'
          },
          {
            event: 'ready_for_review',
            created_at: '2023-05-12T12:00:00Z'
          }
        ],
        reviewEvents: [
          { submitted_at: '2023-05-12T13:00:00Z' }
        ],
        expected: {
          metricType: 'time_to_first_review',
          repository: 'owner/repo',
          prNumber: 125,
          prUrl: 'https://github.com/owner/repo/pull/125',
          prCreator: 'author',
          targetBranch: 'main',
          readyTime: new Date('2023-05-12T12:00:00Z'),
          firstReviewTime: new Date('2023-05-12T13:00:00Z'),
          reviewDate: '2023-05-12',
          pickupTimeSeconds: 3600, // 1 hour = 3600 seconds
          readyEventType: 'ready_for_review event'
        }
      },
      {
        name: 'PR with ready_for_review event after the first review',
        pr: {
          number: 126,
          html_url: 'https://github.com/owner/repo/pull/126',
          draft: false,
          created_at: '2023-05-16T09:00:00Z',
          user: { login: 'author' },
          base: {
            ref: 'main',
            repo: {
              name: 'repo',
              owner: { login: 'owner' }
            }
          }
        },
        timelineEvents: [
          {
            event: 'convert_to_draft',
            created_at: '2023-05-16T10:00:00Z'
          },
          {
            event: 'ready_for_review',
            created_at: '2023-05-16T12:00:00Z'
          }
        ],
        reviewEvents: [
          { submitted_at: '2023-05-16T11:00:00Z' }
        ],
        expected: {
          metricType: 'time_to_first_review',
          repository: 'owner/repo',
          prNumber: 126,
          prUrl: 'https://github.com/owner/repo/pull/126',
          prCreator: 'author',
          targetBranch: 'main',
          readyTime: new Date('2023-05-16T09:00:00Z'),
          firstReviewTime: new Date('2023-05-16T11:00:00Z'),
          reviewDate: '2023-05-16',
          pickupTimeSeconds: 7200, // 2 hours = 7200 seconds
          readyEventType: 'PR creation (not draft)'
        }
      },
      {
        name: 'PR with no ready_for_review events and created as draft',
        pr: {
          number: 127,
          html_url: 'https://github.com/owner/repo/pull/127',
          draft: true,
          created_at: '2023-05-14T09:00:00Z',
          user: { login: 'author' },
          base: {
            ref: 'main',
            repo: {
              name: 'repo',
              owner: { login: 'owner' }
            }
          }
        },
        timelineEvents: [],
        reviewEvents: [
          { submitted_at: '2023-05-14T11:00:00Z' }
        ],
        expected: null // Should return null because no ready event was found
      },
      {
        name: 'PR with no reviews',
        pr: {
          number: 128,
          html_url: 'https://github.com/owner/repo/pull/128',
          draft: false,
          created_at: '2023-05-15T09:00:00Z',
          user: { login: 'author' },
          base: {
            ref: 'main',
            repo: {
              name: 'repo',
              owner: { login: 'owner' }
            }
          }
        },
        timelineEvents: [],
        reviewEvents: [],
        expected: null // Should return null because no reviews were found
      },
      {
        name: 'PR with multiple reviews - only first one should be counted',
        pr: {
          number: 129,
          html_url: 'https://github.com/owner/repo/pull/129',
          draft: false,
          created_at: '2023-05-16T09:00:00Z',
          user: { login: 'author' },
          base: {
            ref: 'main',
            repo: {
              name: 'repo',
              owner: { login: 'owner' }
            }
          }
        },
        timelineEvents: [],
        reviewEvents: [
          { submitted_at: '2023-05-16T10:00:00Z' }, // First review - should be used
          { submitted_at: '2023-05-16T11:00:00Z' }, // Second review - should be ignored
          { submitted_at: '2023-05-16T12:00:00Z' }  // Third review - should be ignored
        ],
        expected: {
          metricType: 'time_to_first_review',
          repository: 'owner/repo',
          prNumber: 129,
          prUrl: 'https://github.com/owner/repo/pull/129',
          prCreator: 'author',
          targetBranch: 'main',
          readyTime: new Date('2023-05-16T09:00:00Z'),
          firstReviewTime: new Date('2023-05-16T10:00:00Z'),
          reviewDate: '2023-05-16',
          pickupTimeSeconds: 3600, // 1 hour = 3600 seconds
          readyEventType: 'PR creation (not draft)'
        }
      },
      {
        name: 'PR ready on Saturday, reviewed on Sunday 3 weeks later (should exclude weekend days)',
        pr: {
          number: 136,
          html_url: 'https://github.com/owner/repo/pull/136',
          draft: false,
          created_at: '2023-05-20T14:00:00Z', // Saturday
          user: { login: 'author' },
          base: {
            ref: 'main',
            repo: {
              name: 'repo',
              owner: { login: 'owner' }
            }
          }
        },
        timelineEvents: [],
        reviewEvents: [
          { submitted_at: '2023-06-11T14:00:00Z' } // Sunday, 3 weeks later
        ],
        expected: {
          metricType: 'time_to_first_review',
          repository: 'owner/repo',
          prNumber: 136,
          prUrl: 'https://github.com/owner/repo/pull/136',
          prCreator: 'author',
          targetBranch: 'main',
          readyTime: new Date('2023-05-20T14:00:00Z'),
          firstReviewTime: new Date('2023-06-11T14:00:00Z'),
          reviewDate: '2023-06-11',
          pickupTimeSeconds: 1296000, // 15 days = 1296000 seconds (3 weeks of 5 working days)
          readyEventType: 'PR creation (not draft)'
        }
      },
      {
        name: 'PR ready on Sunday, reviewed on Monday (should use end of Sunday as ready time)',
        pr: {
          number: 134,
          html_url: 'https://github.com/owner/repo/pull/134',
          draft: false,
          created_at: '2023-05-21T14:00:00Z', // Sunday
          user: { login: 'author' },
          base: {
            ref: 'main',
            repo: {
              name: 'repo',
              owner: { login: 'owner' }
            }
          }
        },
        timelineEvents: [],
        reviewEvents: [
          { submitted_at: '2023-05-22T10:00:00Z' } // Monday
        ],
        expected: {
          metricType: 'time_to_first_review',
          repository: 'owner/repo',
          prNumber: 134,
          prUrl: 'https://github.com/owner/repo/pull/134',
          prCreator: 'author',
          targetBranch: 'main',
          readyTime: new Date('2023-05-21T14:00:00Z'),
          firstReviewTime: new Date('2023-05-22T10:00:00Z'),
          reviewDate: '2023-05-22',
          pickupTimeSeconds: 36000, // 10 hours = 36000 seconds (from end of Sunday to Monday 10am)
          readyEventType: 'PR creation (not draft)'
        }
      },
      {
        name: 'PR ready on Sunday, reviewed on next Saturday (should exclude weekend days)',
        pr: {
          number: 135,
          html_url: 'https://github.com/owner/repo/pull/135',
          draft: false,
          created_at: '2023-05-21T14:00:00Z', // Sunday
          user: { login: 'author' },
          base: {
            ref: 'main',
            repo: {
              name: 'repo',
              owner: { login: 'owner' }
            }
          }
        },
        timelineEvents: [],
        reviewEvents: [
          { submitted_at: '2023-05-27T14:00:00Z' } // Saturday
        ],
        expected: {
          metricType: 'time_to_first_review',
          repository: 'owner/repo',
          prNumber: 135,
          prUrl: 'https://github.com/owner/repo/pull/135',
          prCreator: 'author',
          targetBranch: 'main',
          readyTime: new Date('2023-05-21T14:00:00Z'),
          firstReviewTime: new Date('2023-05-27T14:00:00Z'),
          reviewDate: '2023-05-27',
          pickupTimeSeconds: 432000, // 5 days = 432000 seconds (6 days - 1 weekend day)
          readyEventType: 'PR creation (not draft)'
        }
      },
      {
        name: 'PR ready on Friday, reviewed on Monday (should subtract weekend days)',
        pr: {
          number: 130,
          html_url: 'https://github.com/owner/repo/pull/130',
          draft: false,
          created_at: '2023-05-19T14:00:00Z', // Friday
          user: { login: 'author' },
          base: {
            ref: 'main',
            repo: {
              name: 'repo',
              owner: { login: 'owner' }
            }
          }
        },
        timelineEvents: [],
        reviewEvents: [
          { submitted_at: '2023-05-22T10:00:00Z' } // Monday
        ],
        expected: {
          metricType: 'time_to_first_review',
          repository: 'owner/repo',
          prNumber: 130,
          prUrl: 'https://github.com/owner/repo/pull/130',
          prCreator: 'author',
          targetBranch: 'main',
          readyTime: new Date('2023-05-19T14:00:00Z'),
          firstReviewTime: new Date('2023-05-22T10:00:00Z'),
          reviewDate: '2023-05-22',
          pickupTimeSeconds: 72000, // 20 hours = 72000 seconds (3 days - 2 weekend days)
          readyEventType: 'PR creation (not draft)'
        }
      },
      {
        name: 'PR ready on Saturday, reviewed on Monday (should use end of Sunday as ready time)',
        pr: {
          number: 131,
          html_url: 'https://github.com/owner/repo/pull/131',
          draft: false,
          created_at: '2023-05-20T14:00:00Z', // Saturday
          user: { login: 'author' },
          base: {
            ref: 'main',
            repo: {
              name: 'repo',
              owner: { login: 'owner' }
            }
          }
        },
        timelineEvents: [],
        reviewEvents: [
          { submitted_at: '2023-05-22T10:00:00Z' } // Monday
        ],
        expected: {
          metricType: 'time_to_first_review',
          repository: 'owner/repo',
          prNumber: 131,
          prUrl: 'https://github.com/owner/repo/pull/131',
          prCreator: 'author',
          targetBranch: 'main',
          readyTime: new Date('2023-05-20T14:00:00Z'),
          firstReviewTime: new Date('2023-05-22T10:00:00Z'),
          reviewDate: '2023-05-22',
          pickupTimeSeconds: 36000, // 10 hours = 36000 seconds (from end of Sunday to Monday 10am)
          readyEventType: 'PR creation (not draft)'
        }
      },
      {
        name: 'PR ready on Saturday, reviewed on Sunday (should have 0 pickup time)',
        pr: {
          number: 132,
          html_url: 'https://github.com/owner/repo/pull/132',
          draft: false,
          created_at: '2023-05-20T14:00:00Z', // Saturday
          user: { login: 'author' },
          base: {
            ref: 'main',
            repo: {
              name: 'repo',
              owner: { login: 'owner' }
            }
          }
        },
        timelineEvents: [],
        reviewEvents: [
          { submitted_at: '2023-05-21T14:00:00Z' } // Sunday
        ],
        expected: {
          metricType: 'time_to_first_review',
          repository: 'owner/repo',
          prNumber: 132,
          prUrl: 'https://github.com/owner/repo/pull/132',
          prCreator: 'author',
          targetBranch: 'main',
          readyTime: new Date('2023-05-20T14:00:00Z'),
          firstReviewTime: new Date('2023-05-21T14:00:00Z'),
          reviewDate: '2023-05-21',
          pickupTimeSeconds: 0, // 0 seconds (both on weekend)
          readyEventType: 'PR creation (not draft)'
        }
      },
      {
        name: 'PR ready on weekday, reviewed after multiple weekends (should subtract weekend days)',
        pr: {
          number: 133,
          html_url: 'https://github.com/owner/repo/pull/133',
          draft: false,
          created_at: '2023-05-17T14:00:00Z', // Wednesday
          user: { login: 'author' },
          base: {
            ref: 'main',
            repo: {
              name: 'repo',
              owner: { login: 'owner' }
            }
          }
        },
        timelineEvents: [],
        reviewEvents: [
          { submitted_at: '2023-05-29T14:00:00Z' } // Monday, 12 days later
        ],
        expected: {
          metricType: 'time_to_first_review',
          repository: 'owner/repo',
          prNumber: 133,
          prUrl: 'https://github.com/owner/repo/pull/133',
          prCreator: 'author',
          targetBranch: 'main',
          readyTime: new Date('2023-05-17T14:00:00Z'),
          firstReviewTime: new Date('2023-05-29T14:00:00Z'),
          reviewDate: '2023-05-29',
          pickupTimeSeconds: 691200, // 8 days = 691200 seconds (12 days - 4 weekend days)
          readyEventType: 'PR creation (not draft)'
        }
      }
    ];

    // Run each test case
    test.each(testCases)('$name', ({ pr, timelineEvents, reviewEvents, expected }) => {
      const result = githubClient.calculatePickupTime(pr, timelineEvents, reviewEvents);

      if (expected === null) {
        expect(result).toBeNull();
      } else {
        // Compare date objects separately
        expect(result.readyTime).toEqual(expected.readyTime);
        expect(result.firstReviewTime).toEqual(expected.firstReviewTime);

        // Compare the rest of the properties
        expect({
          ...result,
          readyTime: undefined,
          firstReviewTime: undefined
        }).toEqual({
          ...expected,
          readyTime: undefined,
          firstReviewTime: undefined
        });
      }
    });
  });
});
