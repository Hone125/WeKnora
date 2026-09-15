// 热缓存命中实验（课题三 · 成本可观测）。
//
// 目的：在同一 server 进程内、用同一份默认数据集连续跑两遍评测，
// 第二遍应命中进程内/DB 缓存，HTTP 调用次数骤降——用「直接计数」量化
// 缓存到底省了多少次真实 provider 往返，而不是靠缓存表行数猜。
//
// 前置：server 已启动、账号已注册（同 scripts/ci-eval.sh），环境变量
// EVAL_EMAIL / EVAL_PASSWORD / BASE_URL 已注入。本脚本只做「跑两遍 + 对比」，
// 不负责起服务、不负责开户。
//
// 用法（CI 内）：node scripts/cache-hit-eval.mjs
// 退出码：0 = 两遍均成功；2 = 登录/评测/数据错误。
import { runEvaluation } from './eval-runner.mjs';

const env = process.env;
const common = {
  baseURL: env.BASE_URL || 'http://localhost:8080',
  email: env.EVAL_EMAIL,
  password: env.EVAL_PASSWORD,
  datasetID: 'default',
  timeoutMs: Number(env.EVAL_TIMEOUT_MS || 900000),
  requestTimeoutMs: Number(env.EVAL_REQUEST_TIMEOUT_MS || 30000),
  pollMs: Number(env.EVAL_POLL_MS || 2000),
};

if (!common.email || !common.password) {
  console.error('缺少 EVAL_EMAIL / EVAL_PASSWORD（同 ci-eval.sh 的环境变量约定）');
  process.exit(2);
}

function report(label, payload) {
  const m = payload.data?.metric?.embedding_measurement ?? null;
  const c = payload.data?.cost ?? null;
  console.log(`[${label}] embedding_measurement=${JSON.stringify(m)}`);
  console.log(`[${label}] cost=${JSON.stringify(c)}`);
  return { embedding_measurement: m, cost: c };
}

try {
  const cold = report('冷缓存', await runEvaluation({
    ...common, log: (...a) => console.log('[冷缓存]', ...a),
  }));
  const warm = report('热缓存', await runEvaluation({
    ...common, log: (...a) => console.log('[热缓存]', ...a),
  }));

  const coldHTTP = cold.embedding_measurement?.http_attempts ?? 0;
  const warmHTTP = warm.embedding_measurement?.http_attempts ?? 0;
  const warmMem = warm.embedding_measurement?.memory_hits ?? 0;
  const warmDB = warm.embedding_measurement?.database_hits ?? 0;
  console.log('CACHE_HIT_SUMMARY=' + JSON.stringify({ cold, warm }, null, 2));
  console.log(`结论：冷缓存 HTTP 尝试 ${coldHTTP} 次 → 热缓存 HTTP 尝试 ${warmHTTP} 次，` +
    `内存命中 ${warmMem} 次、DB 命中 ${warmDB} 次`);
  process.exit(0);
} catch (error) {
  console.error(`热缓存实验失败：${error.message}`);
  process.exit(2);
}
