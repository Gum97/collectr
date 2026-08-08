// Redirect hot path.
//
// The target from docs/02-estimation.md: p99 under 80ms at a burst of 500 RPS.
// That is the number the whole caching design exists to hit, so it is the first
// thing worth measuring and the first thing that should fail if something
// regresses.
//
//   docker compose --profile load run --rm k6 run /scripts/redirect.js
import http from 'k6/http';
import { check } from 'k6';
import { Counter, Rate } from 'k6/metrics';

// 404 and 410 are correct answers here, not failures: the scenario deliberately
// asks for codes that do not exist and one that has expired. Without this, k6's
// default failure rate measures the test's own design rather than the system.
http.setResponseCallback(http.expectedStatuses(302, 404, 410));

const gone = new Counter('redirect_gone');
const missing = new Counter('redirect_missing');
const wrongStatus = new Rate('redirect_wrong_status');

const BASE = __ENV.BASE_URL || 'http://caddy';
const HOST = __ENV.HOST_HEADER || 'localhost';
const LINKS = Number(__ENV.LINKS || 500);

export const options = {
  // p99 is the number the design is stated in, so it has to appear in the
  // summary rather than only inside a threshold.
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  scenarios: {
    // Warm first: a cold cache measures the database, not the design. Real
    // traffic to a link arrives after somebody has already clicked it.
    warmup: {
      executor: 'constant-arrival-rate',
      rate: 50, timeUnit: '1s', duration: '20s',
      preAllocatedVUs: 20, maxVUs: 50,
      exec: 'redirect', tags: { phase: 'warmup' },
    },
    // The burst the design is sized for: a QR code on a conference stand.
    burst: {
      executor: 'constant-arrival-rate',
      rate: 500, timeUnit: '1s', duration: '60s',
      preAllocatedVUs: 100, maxVUs: 400,
      startTime: '25s',
      exec: 'redirect', tags: { phase: 'burst' },
    },
  },
  thresholds: {
    // Stated as thresholds so the run fails rather than producing numbers that
    // need interpreting.
    'http_req_duration{phase:burst}': ['p(99)<80', 'p(95)<40'],
    'http_req_failed{phase:burst}': ['rate<0.001'],
    redirect_wrong_status: ['rate<0.001'],
  },
  // Redirects are the thing under test, not what the client should follow.
  noVUConnectionReuse: false,
  discardResponseBodies: true,
};

export function redirect() {
  const roll = Math.random();
  let code, want;

  if (roll < 0.02) {
    // A code nobody has: this is what an enumeration scan looks like, and it is
    // the case the negative cache exists for.
    code = 'nosuch' + Math.floor(Math.random() * 1e6);
    want = 404;
  } else if (roll < 0.04) {
    code = 'loadgone';
    want = 410;
  } else {
    const n = Math.floor(Math.random() * LINKS) + 1;
    code = 'load' + String(n).padStart(4, '0');
    want = 302;
  }

  const res = http.get(`${BASE}/r/${code}`, {
    headers: { Host: HOST },
    redirects: 0,
    tags: { name: 'redirect' },
  });

  const ok = res.status === want;
  wrongStatus.add(!ok);
  if (res.status === 410) gone.add(1);
  if (res.status === 404) missing.add(1);

  check(res, {
    'expected status': () => ok,
    'has Location when 302': (r) => r.status !== 302 || !!r.headers['Location'],
  });
}
