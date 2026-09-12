// Shared evaluation client. Node >= 18; no third-party packages required.
import { mkdir, readFile, writeFile, mkdtemp, rm } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const retrieval = ['precision', 'recall', 'ndcg3', 'ndcg10', 'mrr', 'map'];
const generation = ['bleu1', 'bleu2', 'bleu4', 'rouge1', 'rouge2', 'rougel'];

function positive(value, name) {
  if (!Number.isSafeInteger(value) || value <= 0) throw new Error(`${name} 必须为正整数`);
  return value;
}

export function validateResult(payload, taskID) {
  const d = payload?.data;
  const t = d?.task;
  if (payload?.success === false || !t || t.id !== taskID || t.status !== 2) throw new Error('评测未成功，或返回了其他任务的结果');
  if (!Number.isSafeInteger(t.total) || t.total <= 0 || t.finished !== t.total) throw new Error('评测题目数量不完整');
  for (const [group, names] of [['retrieval_metrics', retrieval], ['generation_metrics', generation]]) {
    for (const name of names) {
      const v = d.metric?.[group]?.[name];
      if (typeof v !== 'number' || !Number.isFinite(v) || v < 0 || v > 1) throw new Error(`缺失或无效指标：${group}.${name}`);
    }
  }
  for (const name of ['prompt_tokens', 'completion_tokens', 'total_tokens', 'latency_ms']) {
    if (!Number.isSafeInteger(d.cost?.[name]) || d.cost[name] < 0) throw new Error(`缺失或无效用量/耗时：${name}`);
  }
  if (d.cost.total_tokens !== d.cost.prompt_tokens + d.cost.completion_tokens) throw new Error('Token 总量与输入、输出之和不一致');
  return payload;
}

export async function runEvaluation({ baseURL = 'http://localhost:8080', email, password,
  datasetID = 'default', chatID = '', rerankID = '', knowledgeBaseID = '',
  timeoutMs = 900000, requestTimeoutMs = 30000, pollMs = 2000, log = () => {} }) {
  positive(timeoutMs, '总超时'); positive(requestTimeoutMs, '请求超时'); positive(pollMs, '轮询间隔');
  if (!email || !password) throw new Error('请通过 EVAL_EMAIL、EVAL_PASSWORD 设置评测账号');
  // The server currently implements only the bundled default dataset.
  if (datasetID !== 'default') throw new Error('当前后端仅实现 default 数据集，拒绝将其他名称冒充独立数据集');
  const base = new URL(baseURL);
  if (!['http:', 'https:'].includes(base.protocol) || base.username || base.password || base.search || base.hash) throw new Error('BASE_URL 必须是不含凭据和查询参数的 HTTP(S) 地址');
  const deadline = Date.now() + timeoutMs;
  let taskID = '';
  async function request(path, method = 'GET', body, token) {
    const remaining = deadline - Date.now();
    if (remaining <= 0) throw new Error(`评测等待超时${taskID ? `，任务 ${taskID} 可能仍在服务器运行` : ''}`);
    try {
      const response = await fetch(`${base.href.replace(/\/$/, '')}${path}`, {
        method, redirect: 'error', signal: AbortSignal.timeout(Math.min(remaining, requestTimeoutMs)),
        headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
        ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
      });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const data = await response.json();
      if (data?.success === false) throw new Error('服务端拒绝请求');
      return data;
    } catch (err) {
      // Never echo response bodies, request bodies, tokens, or redirect targets.
      const reason = /^HTTP \d+$/.test(err.message) ? err.message : '连接超时、响应无效或请求被拒绝';
      throw new Error(`${method} ${path.split('?')[0]}：${reason}${taskID ? '；已有任务可能仍在服务器运行' : ''}`);
    }
  }
  await request('/health');
  const login = await request('/api/v1/auth/login', 'POST', { email, password });
  if (typeof login?.token !== 'string' || !login.token.trim() || login.token === 'undefined') throw new Error('登录响应缺少有效 token');
  const created = await request('/api/v1/evaluation', 'POST', {
    dataset_id: datasetID, knowledge_base_id: knowledgeBaseID, chat_id: chatID, rerank_id: rerankID,
  }, login.token);
  taskID = created?.data?.task?.id;
  if (typeof taskID !== 'string' || !taskID || /[\x00-\x1f\x7f]/.test(taskID)) throw new Error('触发响应缺少有效任务 ID');
  log(`任务已创建：${taskID}`);
  for (;;) {
    const result = await request(`/api/v1/evaluation?task_id=${encodeURIComponent(taskID)}`, 'GET', undefined, login.token);
    const task = result?.data?.task;
    if (!task || task.id !== taskID || ![0, 1, 2, 3].includes(task.status)) throw new Error('轮询响应中的任务或状态无效');
    if (task.status === 3) throw new Error(`评测任务失败：${taskID}`);
    if (task.status === 2) return validateResult(result, taskID);
    log(`评测进度：${task.finished ?? '?'} / ${task.total ?? '?'}，状态 ${task.status}`);
    const remaining = deadline - Date.now();
    if (remaining <= 0) throw new Error(`评测等待超时，任务 ${taskID} 可能仍在服务器运行`);
    await new Promise(resolve => setTimeout(resolve, Math.min(pollMs, remaining)));
  }
}

function redact(value) {
  if (Array.isArray(value)) return value.map(redact);
  if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value)
    .filter(([key]) => !/api.?key|password|secret|authorization|access.?token|refresh.?token/i.test(key))
    .map(([key, entry]) => [key, redact(entry)]));
  return value;
}

export async function main(args = process.argv.slice(2), env = process.env) {
  const gate = args.includes('--gate');
  if (args.some(a => a.startsWith('--') && a !== '--gate') || args.filter(a => !a.startsWith('--')).length > 1) throw new Error('用法：node scripts/eval-runner.mjs [default] [--gate]');
  const datasetID = args.find(a => a !== '--gate') || 'default';
  const gateConfig = resolve(root, env.EVAL_GATE_CONFIG || 'eval_gate.json');
  const hashes = {};
  // Fail before spending model quota if local evidence files are missing.
  for (const file of ['dataset/samples/queries.parquet', 'dataset/samples/corpus.parquet',
    'dataset/samples/answers.parquet', 'dataset/samples/qrels.parquet', 'dataset/samples/qas.parquet', 'config/config.yaml']) {
    hashes[file] = createHash('sha256').update(await readFile(join(root, file))).digest('hex');
  }
  if (gate) hashes.gate_config = createHash('sha256').update(await readFile(gateConfig)).digest('hex');
  const outputRoot = resolve(root, env.EVAL_OUTPUT_DIR || 'eval-results');
  await mkdir(outputRoot, { recursive: true });
  const output = await mkdtemp(join(outputRoot, 'run-'));
  const startedAt = new Date().toISOString();
  const git = (...args) => {
    const result = spawnSync('git', args, { cwd: root, encoding: 'utf8', timeout: 10000 });
    return result.status === 0 ? result.stdout.trim() : null;
  };
  const metadata = { started_at: startedAt, client_commit: git('rev-parse', 'HEAD'),
    client_dirty: Boolean(git('status', '--porcelain')), dataset_id: datasetID,
    local_file_sha256: hashes, server_revision_verified: false,
    note: '指纹来自客户端工作区，不能证明远程服务器加载相同版本；结果可能含评测文本，请审阅后分享。' };
  let scratch;
  try {
    let gateBin;
    if (gate) {
      scratch = await mkdtemp(join(tmpdir(), 'weknora-evalgate-'));
      gateBin = join(scratch, process.platform === 'win32' ? 'evalgate.exe' : 'evalgate');
      const built = spawnSync('go', ['build', '-o', gateBin, './cmd/evalgate'], { cwd: root, stdio: 'inherit', timeout: 120000 });
      if (built.status !== 0) throw new Error('门禁工具编译失败或超时，尚未触发付费评测');
    }
    const payload = await runEvaluation({ baseURL: env.BASE_URL, email: env.EVAL_EMAIL, password: env.EVAL_PASSWORD,
      datasetID, chatID: env.EVAL_CHAT_ID || '', rerankID: env.EVAL_RERANK_ID || '', knowledgeBaseID: env.EVAL_KB_ID || '',
      timeoutMs: Number(env.EVAL_TIMEOUT_MS || 900000), requestTimeoutMs: Number(env.EVAL_REQUEST_TIMEOUT_MS || 30000),
      pollMs: Number(env.EVAL_POLL_MS || 2000), log: console.log });
    const resultFile = join(output, 'result.json');
    await writeFile(resultFile, JSON.stringify(redact(payload), null, 2), { mode: 0o600 });
    let exitCode = 0;
    if (gate) {
      const judged = spawnSync(gateBin, ['-config', gateConfig, '-result', resultFile], { cwd: root, encoding: 'utf8', timeout: 30000 });
      await writeFile(join(output, 'gate.json'), judged.stdout || '', { mode: 0o600 });
      if (judged.stdout) console.log(judged.stdout);
      if (![0, 1, 2].includes(judged.status)) throw new Error('门禁执行失败或超时');
      exitCode = judged.status;
    }
    metadata.exit_code = exitCode;
    const { metric, cost, task } = payload.data;
    console.log(JSON.stringify({ task: task.id, retrieval: metric.retrieval_metrics,
      generation: metric.generation_metrics, token_usage: cost, cost_note: 'Token 用量不等于货币费用', evidence: output }, null, 2));
    return exitCode;
  } catch (error) {
    metadata.exit_code = 2;
    // Keep a minimal failure artifact without secrets or server error bodies.
    await writeFile(join(output, 'failure.json'), JSON.stringify({ error: error.message }, null, 2), { mode: 0o600 });
    throw error;
  } finally {
    metadata.finished_at = new Date().toISOString();
    await writeFile(join(output, 'manifest.json'), JSON.stringify(metadata, null, 2), { mode: 0o600 });
    if (scratch) await rm(scratch, { recursive: true, force: true });
    console.log(`证据目录：${output}`);
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().then(code => { process.exitCode = code; }).catch(error => {
    console.error(`评测失败：${error.message}`); process.exitCode = 2;
  });
}
