import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { runEvaluation, validateResult, main } from './eval-runner.mjs';
import { mkdtemp, readdir, readFile, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

function result() {
  return { success: true, data: { task: { id: 'task-1', status: 2, total: 1, finished: 1 },
    metric: { retrieval_metrics: { precision: 1, recall: 1, ndcg3: 1, ndcg10: 1, mrr: 1, map: 1 },
      generation_metrics: { bleu1: 1, bleu2: 1, bleu4: 1, rouge1: 1, rouge2: 1, rougel: 1 } },
    cost: { prompt_tokens: 10, completion_tokens: 2, total_tokens: 12, latency_ms: 15 } } };
}

async function serve(t, overrides = {}) {
  const seen = [];
  const server = createServer(async (req, res) => {
    let body = '';
    for await (const chunk of req) body += chunk;
    seen.push({ url: req.url, method: req.method, body });
    res.setHeader('Content-Type', 'application/json');
    if (overrides.handle?.(req, res, body)) return;
    if (req.url === '/health') return res.end('{"status":"ok"}');
    if (req.url === '/api/v1/auth/login') return res.end(JSON.stringify(overrides.login ?? { token: 'test-token' }));
    if (req.method === 'POST') return res.end(JSON.stringify({ data: { task: { id: 'task-1' } } }));
    res.end(JSON.stringify(overrides.poll ?? result()));
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => { server.closeAllConnections(); server.close(); });
  return { seen, options: { baseURL: `http://127.0.0.1:${server.address().port}`, email: 'test@example.com',
    password: 'quoted"password\\\n', timeoutMs: 3000, requestTimeoutMs: 1000, pollMs: 5 } };
}

test('完整 HTTP 流程正确转义密码，且只触发一次任务', async t => {
  const { seen, options } = await serve(t);
  assert.deepEqual(await runEvaluation(options), result());
  assert.equal(JSON.parse(seen.find(r => r.url.endsWith('/login')).body).password, options.password);
  assert.equal(seen.filter(r => r.method === 'POST' && r.url.endsWith('/evaluation')).length, 1);
});

test('缺失 token 时不触发评测', async t => {
  const { seen, options } = await serve(t, { login: {} });
  await assert.rejects(runEvaluation(options), /token/);
  assert.equal(seen.filter(r => r.url.endsWith('/evaluation')).length, 0);
});

test('失败任务立即报错', async t => {
  const failed = result(); failed.data.task.status = 3;
  const { options } = await serve(t, { poll: failed });
  await assert.rejects(runEvaluation(options), /任务失败/);
});

test('持续运行的任务有总超时，不自动重试创建', async t => {
  const running = result(); running.data.task.status = 1;
  const { options, seen } = await serve(t, { poll: running });
  await assert.rejects(runEvaluation({ ...options, timeoutMs: 150 }), /超时/);
  assert.ok(seen.filter(r => r.method === 'POST' && r.url.endsWith('/evaluation')).length <= 1);
});

test('HTTP 错误不回显可能包含秘密的服务端正文', async t => {
  const { options } = await serve(t, { handle(req, res) {
    if (!req.url.endsWith('/login')) return false;
    res.statusCode = 401; res.end('secret-response-password'); return true;
  } });
  await assert.rejects(runEvaluation(options), err => /HTTP 401/.test(err.message) && !err.message.includes('secret-response-password'));
});

test('不跟随带凭据请求的重定向', async t => {
  const { options } = await serve(t, { handle(req, res) {
    if (!req.url.endsWith('/login')) return false;
    res.writeHead(307, { Location: '/other' }); res.end(); return true;
  } });
  await assert.rejects(runEvaluation(options), /请求被拒绝/);
});

test('完整成绩校验拒绝伪成功', () => {
  for (const mutate of [
    p => delete p.data.metric.retrieval_metrics.recall,
    p => { p.data.metric.retrieval_metrics.recall = null; },
    p => { p.data.task.finished = 0; },
    p => { p.data.task.total = 0; },
    p => { p.data.task.id = 'other-task'; },
    p => delete p.data.cost.latency_ms,
    p => { p.data.cost.total_tokens = 99; },
  ]) {
    const p = result(); mutate(p);
    assert.throws(() => validateResult(p, 'task-1'));
  }
});

test('未实现的数据集名称和非法超时在联网前拒绝', async () => {
  const options = { email: 'x', password: 'x' };
  await assert.rejects(runEvaluation({ ...options, datasetID: 'imaginary' }), /default/);
  await assert.rejects(runEvaluation({ ...options, timeoutMs: NaN }), /正整数/);
});

test('成功运行保存结果与指纹，并删除凭据字段', async t => {
  const payload = result();
  payload.data.params = { api_key: 'do-not-save', nested: { app_secret: 'also-private' }, embedding_top_k: 5 };
  const { options } = await serve(t, { poll: payload });
  const output = await mkdtemp(join(tmpdir(), 'eval-evidence-test-'));
  t.after(() => rm(output, { recursive: true, force: true }));
  assert.equal(await main(['default'], { BASE_URL: options.baseURL, EVAL_EMAIL: options.email,
    EVAL_PASSWORD: options.password, EVAL_OUTPUT_DIR: output }), 0);
  const runs = await readdir(output);
  assert.equal(runs.length, 1);
  const saved = await readFile(join(output, runs[0], 'result.json'), 'utf8');
  assert.ok(!saved.includes('do-not-save') && !saved.includes('also-private'));
  const manifest = JSON.parse(await readFile(join(output, runs[0], 'manifest.json'), 'utf8'));
  assert.equal(manifest.exit_code, 0);
  assert.equal(manifest.server_revision_verified, false);
  assert.match(manifest.local_file_sha256['dataset/samples/corpus.parquet'], /^[0-9a-f]{64}$/);
});

test('门禁模式调用真实 Go 判断器，保存退化证据并返回 1', async t => {
  const payload = result(); payload.data.metric.retrieval_metrics.recall = 0.1;
  const { options } = await serve(t, { poll: payload });
  const output = await mkdtemp(join(tmpdir(), 'eval-gate-integration-'));
  t.after(() => rm(output, { recursive: true, force: true }));
  const config = join(output, 'config.json');
  await writeFile(config, JSON.stringify({ baseline: { recall: 0.5 }, thresholds: { recall: 0.05 } }));
  assert.equal(await main(['default', '--gate'], { BASE_URL: options.baseURL, EVAL_EMAIL: options.email,
    EVAL_PASSWORD: options.password, EVAL_OUTPUT_DIR: output, EVAL_GATE_CONFIG: config }), 1);
  const run = (await readdir(output)).find(name => name.startsWith('run-'));
  const gate = JSON.parse(await readFile(join(output, run, 'gate.json'), 'utf8'));
  assert.equal(gate.passed, false);
  assert.equal(gate.regressions[0].metric, 'recall');
  assert.equal(JSON.parse(await readFile(join(output, run, 'manifest.json'), 'utf8')).exit_code, 1);
});
