// 模型用量账本检查（M2 · 成本可观测 · 验收）。
//
// 在评测跑完后调用：登录临时账号 → 查 GET /api/v1/models/usage → 打印按模型聚合的
// 账本记录。目的：证明真实调用链把 token 量写进了 model_usages 表，并能从接口按
// 模型/时间区间查出来（而不是只停留在代码与单测层面）。
//
// 安全：不打印 token 与密钥；查询失败或账本为空时退出非零（作为「账本未生效」的信号）。
import { writeFile } from 'node:fs/promises';

const baseURL = process.env.BASE_URL || 'http://localhost:8080';
const email = process.env.EVAL_EMAIL;
const password = process.env.EVAL_PASSWORD;

// 与 eval-runner.mjs 同一套脱敏规则：过滤密钥/口令/授权字段。
function redact(value) {
  if (Array.isArray(value)) return value.map(redact);
  if (value && typeof value === 'object') {
    return Object.fromEntries(Object.entries(value)
      .filter(([key]) => !/api.?key|password|secret|authorization|access.?token|refresh.?token/i.test(key))
      .map(([key, entry]) => [key, redact(entry)]));
  }
  return value;
}

async function main() {
  if (!email || !password) throw new Error('请通过 EVAL_EMAIL、EVAL_PASSWORD 设置评测账号');
  const base = baseURL.replace(/\/$/, '');

  async function request(path, method = 'GET', body, token) {
    const resp = await fetch(`${base}${path}`, {
      method,
      redirect: 'error',
      headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
      ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
    });
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    const data = await resp.json();
    if (data?.success === false) throw new Error('服务端拒绝请求');
    return data;
  }

  const login = await request('/api/v1/auth/login', 'POST', { email, password });
  const token = login?.token;
  if (typeof token !== 'string' || !token.trim() || token === 'undefined') {
    throw new Error('登录响应缺少有效 token');
  }

  const usage = await request('/api/v1/models/usage', 'GET', undefined, token);
  const rows = usage?.data;
  const count = Array.isArray(rows) ? rows.length : 0;
  console.log('USAGE_SUMMARY=' + JSON.stringify({ model_usage_rows: count, rows: redact(rows) }));

  if (count === 0) {
    console.error('账本为空：真实调用后 model_usages 未产生记录，M2 验收证据缺失');
    process.exitCode = 2;
    return;
  }
  console.log(`账本查询成功：${count} 条按模型聚合的用量记录（真实调用链写入 model_usages）`);
}

main().catch((error) => {
  console.error(`账本查询失败：${error.message}`);
  process.exitCode = 2;
});
