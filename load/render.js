// Public form rendering.
//
// Target from docs/02-estimation.md: p99 under 300ms at a burst of 200 RPS. This
// is the request between the click and the submission, so its latency is added
// to every conversion, not just to the ones that finish.
//
//   docker compose --profile load run --rm k6 run /scripts/render.js
import http from 'k6/http';
import { check } from 'k6';

const BASE = __ENV.BASE_URL || 'http://caddy';
const HOST = __ENV.HOST_HEADER || 'localhost';
const FORM = __ENV.FORM || 'fm_test';

export const options = {
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  scenarios: {
    render: {
      executor: 'constant-arrival-rate',
      rate: Number(__ENV.RATE || 200), timeUnit: '1s', duration: '45s',
      preAllocatedVUs: 50, maxVUs: 200,
    },
  },
  thresholds: {
    http_req_duration: ['p(99)<300', 'p(95)<150'],
    http_req_failed: ['rate<0.001'],
  },
};

export default function () {
  const res = http.get(`${BASE}/api/pub/forms/${FORM}`, {
    headers: { Host: HOST },
    tags: { name: 'render' },
  });

  check(res, {
    'served': (r) => r.status === 200,
    // The version id must come back on every render: the client echoes it on
    // submit so the answers are validated against the schema it actually drew.
    'pins a version': (r) => r.status !== 200 || !!r.json('version.id'),
    'carries the consent block': (r) => r.status !== 200 || !!r.json('schema.consent'),
  });
}
