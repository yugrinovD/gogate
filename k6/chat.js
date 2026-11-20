import http from 'k6/http';
import { check } from 'k6';

const BASE_URL = __ENV.BASE_URL || 'http://127.0.0.1:8080';
const API_KEY = __ENV.API_KEY || '';
const RATE = Number(__ENV.RATE || 500);
const DURATION = __ENV.DURATION || '2m';

export const options = {
  scenarios: {
    chat: {
      executor: 'constant-arrival-rate',
      rate: RATE,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: Math.max(200, Math.ceil(RATE / 2)),
      maxVUs: Math.max(1000, RATE * 2),
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<30', 'p(99)<50'],
    checks: ['rate>0.99'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

const params = {
  headers: {
    'Authorization': `Bearer ${API_KEY}`,
    'Content-Type': 'application/json',
  },
  tags: { name: 'chat' },
};

export default function () {
  const payload = JSON.stringify({
    model: 'mock',
    messages: [{ role: 'user', content: `hello from k6 ${__VU}-${__ITER}` }],
    max_tokens: 32,
  });
  const res = http.post(`${BASE_URL}/v1/chat/completions`, payload, params);
  check(res, {
    'status is 200': (r) => r.status === 200,
    'has usage': (r) => {
      try {
        return JSON.parse(r.body).usage.total_tokens > 0;
      } catch (e) {
        return false;
      }
    },
  });
}
