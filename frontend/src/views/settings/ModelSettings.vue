<template>
  <div class="model-settings">
    <div class="section-header">
      <div class="section-header__top">
        <div>
          <h2>{{ $t('modelSettings.title') }}</h2>
          <p class="section-description">{{ $t('modelSettings.description') }}</p>
        </div>
        <t-button
          v-if="authStore.hasRole('admin')"
          type="button"
          theme="primary"
          variant="text"
          size="medium"
          class="model-test-trigger"
          @click="showDebugDrawer = true"
        >
          <template #icon><play-circle-icon /></template>
          {{ $t('modelSettings.actions.debugModel') }}
        </t-button>
      </div>

      <div class="builtin-models-hint" role="note">
        <p class="builtin-hint-label">{{ $t('modelSettings.builtinModels.title') }}</p>
        <p class="builtin-hint-text">
          {{ $t(authStore.isSystemAdmin
            ? 'modelSettings.builtinModels.descriptionAdmin'
            : 'modelSettings.builtinModels.description') }}
        </p>
        <a class="doc-link" href="https://github.com/Tencent/WeKnora/blob/main/docs/BUILTIN_MODELS.md" target="_blank"
          rel="noopener noreferrer">
          {{ $t('modelSettings.builtinModels.viewGuide') }}
          <t-icon name="link" class="link-icon" />
        </a>
      </div>
    </div>

    <t-tabs v-model="activeTypeFilter" class="model-type-tabs" data-guide="settings-models">
      <t-tab-panel value="all" :label="`${$t('common.all')}(${allLegacyModels.length})`" />
      <t-tab-panel value="chat" :label="`${$t('modelSettings.typeShort.chat')}(${countByType('chat')})`" />
      <t-tab-panel value="embedding"
        :label="`${$t('modelSettings.typeShort.embedding')}(${countByType('embedding')})`" />
      <t-tab-panel value="rerank" :label="`${$t('modelSettings.typeShort.rerank')}(${countByType('rerank')})`" />
      <t-tab-panel value="vllm" :label="`${$t('modelSettings.typeShort.vllm')}(${countByType('vllm')})`" />
      <t-tab-panel value="asr" :label="`${$t('modelSettings.typeShort.asr')}(${countByType('asr')})`" />
    </t-tabs>

    <!-- 模型用量与成本概览（课题三 M2 成本可观测） -->
    <section class="usage-panel" data-guide="settings-model-usage">
      <div class="usage-panel__header">
        <div>
          <h3 class="usage-panel__title">模型用量与成本</h3>
          <p class="usage-panel__subtitle">{{ usageSubtitle }}</p>
        </div>
        <div class="usage-panel__actions">
          <t-date-range-picker
            v-model="usageTimeRange"
            :placeholder="['开始日期', '结束日期']"
            :disable-date="disableFutureDate"
            clearable
            allow-input
            size="small"
            class="usage-panel__date"
          >
            <template #prefixIcon><t-icon name="time" size="16px" /></template>
          </t-date-range-picker>
          <t-button variant="outline" size="small" :loading="usageLoading" @click="loadUsage">
            <template #icon><t-icon name="refresh" /></template>
            刷新
          </t-button>
        </div>
      </div>

      <t-loading :loading="usageLoading" size="small">
        <p v-if="usageError" class="usage-panel__error">{{ usageError }}</p>
        <t-table
          v-else-if="usageRows.length > 0"
          :data="usageRows"
          :columns="usageColumns"
          row-key="model_id"
          size="small"
          :bordered="false"
          hover
          class="usage-table"
        >
          <template #model_type="{ row }">{{ usageTypeLabel(row.model_type) }}</template>
          <template #call_count="{ row }">{{ formatTokens(row.call_count) }}</template>
          <template #prompt_tokens="{ row }">{{ formatTokens(row.prompt_tokens) }}</template>
          <template #completion_tokens="{ row }">{{ formatTokens(row.completion_tokens) }}</template>
          <template #cache_hit_rate="{ row }">{{ formatHitRate(row.cache_hit_rate) }}</template>
          <template #cost="{ row }">{{ formatCost(row.cost, row.currency) }}</template>
        </t-table>
        <t-empty v-else description="暂无模型调用记录" size="small" class="usage-panel__empty" />
      </t-loading>
    </section>

    <t-loading :loading="loading" size="small" class="model-list-loading">
      <div v-if="!loading && filteredModels.length === 0 && !authStore.hasRole('admin')" class="empty-state">
        <t-empty :description="emptyHint" />
      </div>
      <div v-else-if="!loading" class="model-grid">
        <div v-for="model in filteredModels" :key="`${model._modelType}-${model.id}`" class="model-card" :class="[
          `model-card--${model._modelType}`,
          {
            'model-card--builtin': model.isBuiltin,
            'model-card--clickable': isModelCardClickable(model),
          },
        ]" :role="isModelCardClickable(model) ? 'button' : undefined"
          :tabindex="isModelCardClickable(model) ? 0 : undefined"
          @click="onModelCardClick($event, model._modelType, model)"
          @keydown.enter="onModelCardClick($event, model._modelType, model)">
          <div class="model-card__badge" :aria-label="typeLabel(model._modelType)">
            <t-icon :name="typeIcon(model._modelType)" size="18px" />
          </div>
          <div class="model-card__body">
            <div class="model-card__header">
              <h3 class="model-card__title">{{ modelDisplayName(model) }}</h3>
              <span v-if="model.isBuiltin" class="model-card__lock" :title="$t('modelSettings.builtinTag')"
                :aria-label="$t('modelSettings.builtinTag')">
                <t-icon :name="authStore.isSystemAdmin ? 'edit-1' : 'lock-on'" />
              </span>
              <div v-if="canManageModel(model)" class="model-card__actions" @click.stop>
                <t-dropdown :options="getModelOptions(model._modelType, model)" placement="bottom-right" attach="body"
                  trigger="click"
                  @click="(data: any) => handleMenuAction({ value: data.value }, model._modelType, model)">
                  <t-button variant="text" shape="square" size="small" class="model-card__action-btn model-card__more">
                    <t-icon name="ellipsis" />
                  </t-button>
                </t-dropdown>
                <t-popconfirm
                  v-if="canDeleteModel(model)"
                  :content="$t('modelSettings.confirmDelete', { name: modelDisplayName(model) })"
                  :confirm-btn="{ content: $t('common.delete'), theme: 'danger' }"
                  :cancel-btn="{ content: $t('common.cancel') }"
                  placement="bottom-right"
                  @confirm="deleteModel(model._modelType, model.id)"
                >
                  <t-tooltip :content="$t('common.delete')" placement="top">
                    <t-button
                      theme="danger"
                      shape="square"
                      variant="text"
                      size="small"
                      class="model-card__action-btn model-card__delete"
                      @click.stop
                    >
                      <template #icon><t-icon name="delete" /></template>
                    </t-button>
                  </t-tooltip>
                </t-popconfirm>
              </div>
            </div>
            <p class="model-card__subtitle">
              <span>{{ vendorLabel(model) }}</span>
              <template v-if="model._modelType === 'embedding' && model.dimension">
                <span class="model-card__sep">·</span>
                <span>{{ $t('model.editor.dimensionLabel') }} {{ model.dimension }}</span>
              </template>
              <template v-if="model._modelType === 'chat' && model.supportsVision">
                <span class="model-card__sep">·</span>
                <span class="model-card__vision" :title="$t('model.editor.supportsVisionLabel')"
                  :aria-label="$t('model.editor.supportsVisionLabel')">
                  <t-icon name="image" size="12px" />
                </span>
              </template>
            </p>
          </div>
        </div>
        <button
          v-if="authStore.hasRole('admin')"
          type="button"
          class="model-card model-card--add"
          data-guide="settings-add-model"
          @click="openAddDialog"
        >
          <span class="model-card--add__icon" aria-hidden="true">
            <add-icon />
          </span>
          <span class="model-card--add__label">{{ $t('modelSettings.actions.addModel') }}</span>
        </button>
      </div>
    </t-loading>

    <!-- 模型编辑器抽屉 -->
    <ModelEditorDialog v-model:visible="showDialog" :model-type="currentModelType" :model-data="editingModel"
      @confirm="handleModelSave" />
    <ModelDebugDrawer v-model:visible="showDebugDrawer" :models="allModels" />

  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { AddIcon, PlayCircleIcon } from 'tdesign-icons-vue-next'
import { useI18n } from 'vue-i18n'
import ModelEditorDialog from '@/components/ModelEditorDialog.vue'
import ModelDebugDrawer from '@/components/ModelDebugDrawer.vue'
import { listModels, createModel, updateModel as updateModelAPI, deleteModel as deleteModelAPI, getModelUsage, type ModelConfig, type ModelUsageAggregate } from '@/api/model'
import { useAuthStore } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'

const { t, te } = useI18n()
const authStore = useAuthStore()
const uiStore = useUIStore()
type ModelType = 'chat' | 'embedding' | 'rerank' | 'vllm' | 'asr'
type FilterType = 'all' | ModelType

const showDialog = ref(false)
const showDebugDrawer = ref(false)
const currentModelType = ref<ModelType>('chat')
const editingModel = ref<any>(null)
const loading = ref(true)
const activeTypeFilter = ref<FilterType>('all')

const MODEL_TAB_TYPES: FilterType[] = ['chat', 'embedding', 'rerank', 'vllm', 'asr']
watch(
  () => uiStore.settingsInitialSubSection,
  (sub) => {
    if (sub && MODEL_TAB_TYPES.includes(sub as FilterType)) {
      activeTypeFilter.value = sub as FilterType
    }
  },
  { immediate: true },
)

// 模型列表数据
const allModels = ref<ModelConfig[]>([])

// 模型用量与成本（M2 成本可观测）
const usageLoading = ref(false)
const usageRows = ref<ModelUsageAggregate[]>([])
const usageError = ref('')
// 用量统计时间区间（"YYYY-MM-DD"，空数组 = 最近 7 天）
const usageTimeRange = ref<string[]>([])
const disableFutureDate = { after: new Date(new Date().setHours(23, 59, 59, 999)) }

// 动态说明当前统计区间
const usageSubtitle = computed(() => {
  const [s, e] = usageTimeRange.value || []
  const scope = s && e ? `${s} ~ ${e}` : '最近 7 天'
  return `${scope}的调用量、缓存命中率与费用（费用按模型当前定价即时计算）`
})

// 后端 type → 前端分组 type 的映射
const backendTypeToModelType: Record<string, ModelType> = {
  KnowledgeQA: 'chat',
  Embedding: 'embedding',
  Rerank: 'rerank',
  VLLM: 'vllm',
  ASR: 'asr'
}

// 将后端模型格式转换为旧的前端格式（附带 _modelType 便于渲染）
// apiKey is always blank here: the server's main GET response does not
// include it (see internal/handler/dto/model.go — ModelParametersDTO omits
// secret fields). Credential read/write happens inside the editor dialog
// via the dedicated /credentials subresource.
function convertToLegacyFormat(model: ModelConfig) {
  return {
    id: model.id!,
    name: model.name,
    displayName: model.display_name || '',
    source: model.source,
    modelName: model.name,
    baseUrl: model.parameters.base_url || '',
    apiKey: '',
    provider: model.parameters.provider || '',
    dimension: model.parameters.embedding_parameters?.dimension,
    supportsDimensionOverride: model.parameters.embedding_parameters?.supports_dimension_override || false,
    isBuiltin: model.is_builtin || false,
    supportsVision: model.parameters.supports_vision || false,
    maxConcurrency: model.parameters.max_concurrency,
    customHeaders: model.parameters.custom_headers
      ? Object.entries(model.parameters.custom_headers).map(([key, value]) => ({ key, value: String(value) }))
      : [],
    lkeapRegion: model.parameters.extra_config?.region || 'ap-guangzhou',
    // 原始存库值，编辑弹窗内再 resolve（避免打开时被推断值覆盖）
    thinkingControl: model.parameters.extra_config?.thinking_control,
    _modelType: backendTypeToModelType[model.type] || 'chat' as ModelType,
    // Preserve the credential metadata map so the editor dialog can render
    // the "Configured" state without an extra round-trip.
    credentials: model.credentials,
  }
}

// 平铺 + 过滤
const allLegacyModels = computed(() => allModels.value.map(convertToLegacyFormat))
const filteredModels = computed(() => {
  if (activeTypeFilter.value === 'all') return allLegacyModels.value
  return allLegacyModels.value.filter(m => m._modelType === activeTypeFilter.value)
})

const countByType = (type: ModelType) => allLegacyModels.value.filter(m => m._modelType === type).length

// 类型徽章图标。沿用 TDesign 自带 icon name，避免再引第三方图标包。
const typeIcon = (type: ModelType): string => {
  const map: Record<ModelType, string> = {
    chat: 'chat',
    embedding: 'chart-bubble',
    rerank: 'filter-sort',
    vllm: 'image',
    asr: 'sound',
  }
  return map[type]
}

const typeLabel = (type: ModelType) => {
  const map: Record<ModelType, string> = {
    chat: t('modelSettings.typeShort.chat'),
    embedding: t('modelSettings.typeShort.embedding'),
    rerank: t('modelSettings.typeShort.rerank'),
    vllm: t('modelSettings.typeShort.vllm'),
    asr: t('modelSettings.typeShort.asr')
  }
  return map[type]
}

const sourceLabel = (type: ModelType) => {
  // vllm / asr 的 remote 文案特殊，其余走通用 remote 文案
  if (type === 'vllm' || type === 'asr') {
    return t('modelSettings.source.openaiCompatible')
  }
  return t('modelSettings.source.remote')
}

// Maps a backend `provider` id (e.g. "openai", "aliyun", "weknoracloud")
// to its localized short label. Reuses the same i18n keys the editor's
// provider dropdown uses, so the model card and the editor stay in sync
// when a provider is renamed. Falls back to '' when the backend didn't
// store a provider — caller falls back to sourceLabel().
const providerLabel = (model: any): string => {
  const id = model.provider
  if (!id) return ''
  const key = `model.editor.providers.${id}.label`
  return te(key) ? t(key) : id
}

// What the vendor chip on a card shows. Keeps the chip text uniformly
// short so cards line up:
//   local  → "Ollama"
//   remote → provider's localized short name (e.g. "腾讯云 LKEAP",
//            "阿里云 DashScope"). For the catch-all "generic" provider
//            we render a single short word ("自定义" / "Custom") — the
//            editor dropdown's longer "自定义 (OpenAI兼容接口)" label
//            blows out the card chip row, and the "OpenAI 兼容" framing
//            isn't meaningful to most end users (they didn't pick "I
//            want OpenAI compatibility", they just pasted a base URL).
const vendorLabel = (model: any): string => {
  if (model.source === 'local') return 'Ollama'
  if (model.provider === 'generic') {
    return t('modelSettings.source.custom')
  }
  return providerLabel(model) || sourceLabel(model._modelType)
}

const modelDisplayName = (model: any) => {
  const displayName = typeof model.displayName === 'string' ? model.displayName.trim() : ''
  return displayName || model.name
}

const emptyHint = computed(() => {
  if (activeTypeFilter.value === 'all') return t('modelSettings.chat.empty')
  const map: Record<ModelType, string> = {
    chat: t('modelSettings.chat.empty'),
    embedding: t('modelSettings.embedding.empty'),
    rerank: t('modelSettings.rerank.empty'),
    vllm: t('modelSettings.vllm.empty'),
    asr: t('modelSettings.asr.empty')
  }
  return map[activeTypeFilter.value as ModelType]
})

// 加载模型列表
const loadModels = async () => {
  loading.value = true
  try {
    const models = await listModels()
    allModels.value = models
  } catch (error: any) {
    console.error('加载模型列表失败:', error)
    MessagePlugin.error(error.message)
  } finally {
    loading.value = false
  }
}

// 加载模型用量与成本概览（默认最近 7 天，可按时间区间筛选）
let usageRequestVersion = 0
const loadUsage = async () => {
  const requestVersion = ++usageRequestVersion
  usageLoading.value = true
  usageError.value = ''
  try {
    const [s, e] = usageTimeRange.value || []
    // Picker dates are local calendar days; convert their boundaries to UTC.
    // The server uses [start, end), so end is midnight of the following day.
    const startDate = s ? new Date(`${s}T00:00:00`) : undefined
    const endDate = e ? new Date(`${e}T00:00:00`) : undefined
    if (endDate) endDate.setDate(endDate.getDate() + 1)
    const rows = await getModelUsage(startDate?.toISOString(), endDate?.toISOString())
    if (requestVersion === usageRequestVersion) usageRows.value = rows
  } catch (error: any) {
    if (requestVersion === usageRequestVersion) usageError.value = error?.message || '加载成本数据失败'
  } finally {
    if (requestVersion === usageRequestVersion) usageLoading.value = false
  }
}

// 时间区间变化时自动刷新（清空回退最近 7 天）
watch(usageTimeRange, () => {
  loadUsage()
})

// 千分位格式化
const formatTokens = (n: number): string => {
  if (n == null || Number.isNaN(n)) return '0'
  return Number(n).toLocaleString('zh-CN')
}

// 缓存命中率 0~1 → 百分比字符串
const formatHitRate = (rate: number): string => {
  if (rate == null || Number.isNaN(rate)) return '—'
  return `${(rate * 100).toFixed(1)}%`
}

// 费用格式化：保留 4 位小数（单价按每百万 token 计，单次聚合金额通常很小）
const formatCost = (cost: number, currency: string): string => {
  if (!currency) return '未配置单价'
  if (cost == null || Number.isNaN(cost)) return '—'
  const num = Number(cost)
  const fixed = num >= 1 ? num.toFixed(2) : num.toFixed(4)
  return `${fixed} ${currency || ''}`.trim()
}

// 后端 model_type → 前端展示文案
const usageTypeLabel = (mt: string): string => {
  const map: Record<string, string> = {
    chat: t('modelSettings.typeShort.chat'),
    embedding: t('modelSettings.typeShort.embedding'),
    rerank: t('modelSettings.typeShort.rerank'),
    vllm: t('modelSettings.typeShort.vllm'),
    asr: t('modelSettings.typeShort.asr'),
  }
  return map[mt] || mt
}

// 成本概览表格列定义
const usageColumns = [
  { colKey: 'model_name', title: '模型', width: 200, ellipsis: true },
  { colKey: 'model_type', title: '类型', width: 110 },
  { colKey: 'call_count', title: '调用次数', width: 100, align: 'right' as const },
  { colKey: 'prompt_tokens', title: '输入 Tokens', width: 120, align: 'right' as const },
  { colKey: 'completion_tokens', title: '输出 Tokens', width: 120, align: 'right' as const },
  { colKey: 'cache_hit_rate', title: '缓存命中率', width: 110, align: 'right' as const },
  { colKey: 'cost', title: '费用', width: 120, align: 'right' as const },
]

// 打开添加对话框；类型在抽屉内选择，此处仅按当前 Tab 预填默认值
const openAddDialog = () => {
  currentModelType.value = activeTypeFilter.value === 'all' ? 'chat' : activeTypeFilter.value
  editingModel.value = null
  showDialog.value = true
}

// Tenant Admin+ manages tenant models; only SystemAdmin manages shared
// built-in models. The backend repeats this distinction authoritatively.
const canEditModel = (model: any) =>
  model.isBuiltin ? authStore.isSystemAdmin : authStore.hasRole('admin')

const isModelCardClickable = (model: any) => canEditModel(model)

const canManageModel = (model: any) => canEditModel(model)

// Built-in lifecycle remains deployment-managed (YAML / SQL). The UI only
// exposes configuration and credential editing to SystemAdmin.
const canDeleteModel = (model: any) =>
  authStore.hasRole('admin') && !model.isBuiltin

const onModelCardClick = (event: Event, type: ModelType, model: any) => {
  if (!isModelCardClickable(model)) return
  if (event.type === 'keydown') {
    const ke = event as KeyboardEvent
    if (ke.key !== 'Enter' && ke.key !== ' ') return
    ke.preventDefault()
  }
  const target = event.target as HTMLElement | null
  if (target?.closest('.model-card__actions')) return
  editModel(type, model)
}

// 编辑模型
const editModel = (type: ModelType, model: any) => {
  if (model.isBuiltin && !authStore.isSystemAdmin) {
    MessagePlugin.warning(t('modelSettings.toasts.builtinCannotEdit'))
    return
  }
  if (!model.isBuiltin && !authStore.hasRole('admin')) {
    return
  }
  currentModelType.value = type
  editingModel.value = { ...model }
  showDialog.value = true
}

// 保存模型
const handleModelSave = async (modelData: any) => {
  const saveType: ModelType = modelData.modelType ?? currentModelType.value
  currentModelType.value = saveType

  try {
    if (!modelData.modelName || !modelData.modelName.trim()) {
      MessagePlugin.warning(t('modelSettings.toasts.nameRequired'))
      return
    }

    if (modelData.modelName.trim().length > 100) {
      MessagePlugin.warning(t('modelSettings.toasts.nameTooLong'))
      return
    }

    if (modelData.displayName && modelData.displayName.trim().length > 100) {
      MessagePlugin.warning(t('modelSettings.toasts.displayNameTooLong'))
      return
    }

    if (modelData.source === 'remote') {
      if (!modelData.baseUrl || !modelData.baseUrl.trim()) {
        MessagePlugin.warning(t('modelSettings.toasts.baseUrlRequired'))
        return
      }

      try {
        new URL(modelData.baseUrl.trim())
      } catch {
        MessagePlugin.warning(t('modelSettings.toasts.baseUrlInvalid'))
        return
      }
    }

    if (saveType === 'embedding') {
      if (!modelData.dimension || modelData.dimension < 128 || modelData.dimension > 4096) {
        MessagePlugin.warning(t('modelSettings.toasts.dimensionInvalid'))
        return
      }
    }

    const customHeadersMap: Record<string, string> = {}
    if (Array.isArray(modelData.customHeaders)) {
      for (const item of modelData.customHeaders) {
        const key = (item?.key ?? '').trim()
        const value = (item?.value ?? '').trim()
        if (key && value) {
          customHeadersMap[key] = value
        }
      }
    }

    // api_key flows in only on initial create (modelData.apiKey is wiped on
    // every edit-mode open). Edits to existing models commit credentials via
    // the /credentials subresource (handled inside ModelEditorDialog).
    const trimmedApiKey = (modelData.apiKey ?? '').trim()
    const apiKeyFields: { api_key?: string } =
      !editingModel.value && trimmedApiKey ? { api_key: trimmedApiKey } : {}
    const trimmedAppSecret = (modelData.appSecret ?? '').trim()
    const appSecretFields: { app_secret?: string } =
      !editingModel.value && trimmedAppSecret ? { app_secret: trimmedAppSecret } : {}
    const extraConfig: Record<string, string> = {}
    if (modelData.provider === 'lkeap' && saveType === 'rerank') {
      extraConfig.region = (modelData.lkeapRegion || 'ap-guangzhou').trim()
    }
    if (
      saveType === 'chat'
      && modelData.source === 'remote'
      && modelData.thinkingControl
    ) {
      extraConfig.thinking_control = modelData.thinkingControl
    }
    const extraConfigFields = Object.keys(extraConfig).length > 0
      ? { extra_config: extraConfig }
      : {}

    const apiModelData: ModelConfig = {
      name: modelData.modelName.trim(),
      display_name: modelData.displayName?.trim() || '',
      type: getModelType(saveType),
      source: modelData.source,
      description: '',
      parameters: {
        base_url: modelData.baseUrl?.trim() || '',
        ...apiKeyFields,
        ...appSecretFields,
        provider: modelData.provider || '',
        ...extraConfigFields,
        ...(Object.keys(customHeadersMap).length > 0 ? { custom_headers: customHeadersMap } : {}),
        ...(saveType === 'embedding' && modelData.dimension ? {
          embedding_parameters: {
            dimension: modelData.dimension,
            truncate_prompt_tokens: 0,
            supports_dimension_override: modelData.supportsDimensionOverride ?? false
          }
        } : {}),
        ...(saveType === 'vllm' ? {
          supports_vision: true
        } : saveType === 'chat' ? {
          supports_vision: modelData.supportsVision ?? false
        } : {}),
        // 后台并发上限：仅 chat/embedding/vllm 受治理，>0 才写入（0/空沿用全局默认）。
        ...(['chat', 'embedding', 'vllm'].includes(saveType)
          && Number(modelData.maxConcurrency) > 0
          ? { max_concurrency: Number(modelData.maxConcurrency) }
          : {})
      }
    }

    if (editingModel.value && editingModel.value.id) {
      await updateModelAPI(editingModel.value.id, apiModelData)
      MessagePlugin.success(t('modelSettings.toasts.updated'))
    } else {
      await createModel(apiModelData)
      MessagePlugin.success(t('modelSettings.toasts.added'))
    }

    showDialog.value = false
    await loadModels()
  } catch (error: any) {
    console.error('保存模型失败:', error)
    MessagePlugin.error(error.message || t('modelSettings.toasts.saveFailed'))
  }
}

// 删除模型
const deleteModel = async (_type: ModelType, modelId: string) => {
  const model = allModels.value.find(m => m.id === modelId)
  if (model?.is_builtin) {
    MessagePlugin.warning(t('modelSettings.toasts.builtinCannotDelete'))
    return
  }

  try {
    await deleteModelAPI(modelId)
    MessagePlugin.success(t('modelSettings.toasts.deleted'))
    await loadModels()
  } catch (error: any) {
    console.error('删除模型失败:', error)
    MessagePlugin.error(error.message || t('modelSettings.toasts.deleteFailed'))
  }
}

// 获取模型操作菜单选项
const getModelOptions = (type: ModelType, model: any) => {
  const options: any[] = []

  if (model.isBuiltin) {
    if (authStore.isSystemAdmin) {
      options.push({
        content: t('common.edit'),
        value: `edit-${type}-${model.id}`
      })
    }
    return options
  }

  // Models are tenant-wide infrastructure (LLM credentials); the
  // backend gates every mutation behind Admin+ (see RegisterModelRoutes).
  // Non-Admins get an empty action menu — viewing is fine, but editing,
  // copying (also goes through createModel), and deleting are not.
  if (!authStore.hasRole('admin')) {
    return options
  }

  options.push({
    content: t('common.edit'),
    value: `edit-${type}-${model.id}`
  })

  options.push({
    content: t('common.copy'),
    value: `copy-${type}-${model.id}`
  })

  return options
}

// 处理菜单操作
const handleMenuAction = (data: { value: string }, type: ModelType, model: any) => {
  const value = data.value

  if (value.indexOf('edit-') === 0) {
    editModel(type, model)
  } else if (value.indexOf('copy-') === 0) {
    copyModel(type, model.id)
  }
}

// 生成不重复的复制名称
const generateCopyName = (originalName: string): string => {
  const suffix = t('modelSettings.copySuffix')
  const existingNames = new Set(allModels.value.map(m => m.name))
  let candidate = `${originalName}${suffix}`
  let counter = 2
  while (existingNames.has(candidate)) {
    candidate = `${originalName}${suffix} ${counter}`
    counter += 1
  }
  return candidate
}

// 复制模型
const copyModel = async (_type: ModelType, modelId: string) => {
  const source = allModels.value.find(m => m.id === modelId)
  if (!source) {
    return
  }
  if (source.is_builtin) {
    MessagePlugin.warning(t('modelSettings.toasts.builtinCannotCopy'))
    return
  }

  try {
    const newModel: ModelConfig = {
      name: generateCopyName(source.name),
      display_name: source.display_name || '',
      type: source.type,
      source: source.source,
      description: source.description || '',
      parameters: JSON.parse(JSON.stringify(source.parameters || {}))
    }

    await createModel(newModel)
    MessagePlugin.success(t('modelSettings.toasts.copied'))
    await loadModels()
  } catch (error: any) {
    console.error('复制模型失败:', error)
    MessagePlugin.error(error.message || t('modelSettings.toasts.copyFailed'))
  }
}

// 获取后端模型类型
function getModelType(type: ModelType): 'KnowledgeQA' | 'Embedding' | 'Rerank' | 'VLLM' | 'ASR' {
  const typeMap = {
    chat: 'KnowledgeQA' as const,
    embedding: 'Embedding' as const,
    rerank: 'Rerank' as const,
    vllm: 'VLLM' as const,
    asr: 'ASR' as const
  }
  return typeMap[type]
}

onMounted(() => {
  loadModels()
  loadUsage()
})
</script>

<style lang="less" scoped>
.model-settings {
  width: 100%;
}

.section-header {
  margin-bottom: 28px;

  h2 {
    font-size: 20px;
    font-weight: 600;
    color: var(--td-text-color-primary);
    margin: 0 0 8px 0;
  }

  .section-description {
    font-size: 14px;
    color: var(--td-text-color-secondary);
    margin: 0;
    line-height: 1.6;
  }
}

.section-header__top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 20px;
}

.model-test-trigger {
  --td-bg-color-container-hover: transparent;
  flex-shrink: 0;
  padding-left: 0;
  padding-right: 0;
  font-weight: 600;

  &:hover,
  &:focus,
  &.t-is-active,
  &:active {
    background-color: transparent !important;
    color: var(--td-brand-color-hover);
  }

  &:active {
    color: var(--td-brand-color-active);
  }
}

.builtin-models-hint {
  margin-top: 12px;
  padding: 10px 12px;
  background: var(--td-bg-color-secondarycontainer);
  border: 1px solid var(--td-component-stroke);
  border-radius: 6px;
}

.builtin-hint-label {
  margin: 0 0 4px 0;
  font-size: 12px;
  font-weight: 500;
  color: var(--td-text-color-placeholder);
  letter-spacing: 0.02em;
}

.builtin-hint-text {
  margin: 0 0 6px 0;
  font-size: 13px;
  line-height: 1.55;
  color: var(--td-text-color-secondary);
}

.builtin-models-hint .doc-link {
  font-size: 13px;
}

.model-list-loading {
  min-height: 120px;
}

.model-type-tabs {
  margin-bottom: 16px;

  :deep(.t-tabs__nav-item) {
    font-size: 13px;
  }

  :deep(.t-tabs__nav-item-wrapper) {
    padding: 0 12px;
    margin: 0;
  }

  :deep(.t-tabs__operations) {
    display: none;
  }

  :deep(.t-tabs__nav-scroll) {
    overflow-x: auto;
    scrollbar-width: none;

    &::-webkit-scrollbar {
      display: none;
    }
  }

  :deep(.t-tabs__content) {
    display: none;
  }
}

.model-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 12px;

  .model-card--add {
    width: 100%;
    height: 100%;
  }
}

// 模型卡片 —— 可选类型徽章（仅「全部」Tab）+ 标题 + 一行副标题
.model-card {
  position: relative;
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 14px 16px;
  border: 1px solid var(--td-component-stroke);
  border-radius: 10px;
  background: var(--td-bg-color-container);
  transition: border-color 0.18s ease, box-shadow 0.18s ease, transform 0.18s ease;
  min-width: 0;

  &:hover {
    border-color: var(--td-brand-color-3, var(--td-brand-color));
    box-shadow: 0 4px 14px rgba(15, 23, 42, 0.06);
  }

  &--add {
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 8px;
    min-height: 68px;
    border-style: dashed;
    background: transparent;
    color: var(--td-text-color-placeholder);
    cursor: pointer;
    font: inherit;
    text-align: center;

    &:hover,
    &:focus-visible {
      color: var(--td-brand-color);
      border-color: var(--td-brand-color);
      background: color-mix(in srgb, var(--td-brand-color) 6%, transparent);
      box-shadow: none;
    }

    &:focus-visible {
      outline: 2px solid var(--td-brand-color);
      outline-offset: 2px;
    }

    &__icon {
      display: flex;
      align-items: center;
      justify-content: center;
      width: 32px;
      height: 32px;
      border-radius: 8px;
      background: color-mix(in srgb, var(--td-brand-color) 10%, transparent);
      color: var(--td-brand-color);
      font-size: 18px;
    }

    &__label {
      font-size: 13px;
      font-weight: 500;
      line-height: 1.4;
    }
  }

  &--builtin {
    background: var(--td-bg-color-secondarycontainer);

    &:hover {
      box-shadow: none;
      border-color: var(--td-component-stroke);
    }
  }

  &--clickable {
    cursor: pointer;

    &:hover {
      border-color: var(--td-brand-color-3, var(--td-brand-color));
      box-shadow: 0 4px 14px rgba(15, 23, 42, 0.06);
    }

    &:focus-visible {
      outline: 2px solid var(--td-brand-color);
      outline-offset: 2px;
    }
  }
}

.model-card__badge {
  flex-shrink: 0;
  width: 36px;
  height: 36px;
  border-radius: 9px;
  display: flex;
  align-items: center;
  justify-content: center;
  margin-top: 1px;
  // 默认底色，被 type 修饰覆盖
  background: rgba(0, 82, 217, 0.1);
  color: #0052D9;
}

// 5 种类型的徽章配色 —— 比原 tag 配色饱和度低一档，避免炫光
.model-card--chat .model-card__badge {
  background: rgba(0, 82, 217, 0.1);
  color: #0052D9;
}

.model-card--embedding .model-card__badge {
  background: rgba(98, 53, 187, 0.1);
  color: #6235BB;
}

.model-card--rerank .model-card__badge {
  background: rgba(184, 92, 0, 0.1);
  color: #B85C00;
}

.model-card--vllm .model-card__badge {
  background: rgba(201, 62, 62, 0.1);
  color: #C93E3E;
}

.model-card--asr .model-card__badge {
  background: rgba(17, 128, 83, 0.1);
  color: #118053;
}

.model-card__body {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  justify-content: center;
  gap: 2px;
}

.model-card__header {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.model-card__title {
  flex: 1;
  min-width: 0;
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  line-height: 1.4;
  color: var(--td-text-color-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/*
  Built-in lock indicator. Most cards in a typical install ARE built-in,
  so loud styling everywhere becomes noise — instead the lock is muted
  and small by default, and lights up on hover. The signal that matters
  to users is "which models did I add" → user-added cards stand out by
  the absence of the lock.
*/
.model-card__lock {
  flex-shrink: 0;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  color: var(--td-text-color-placeholder);
  opacity: 0.6;
  transition: color 0.15s ease, opacity 0.15s ease;

  .t-icon {
    font-size: 13px;
  }
}

.model-card:hover .model-card__lock {
  opacity: 1;
  color: var(--td-text-color-secondary);
}

.model-card__subtitle {
  margin: 2px 0 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--td-text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.model-card__sep {
  margin: 0 4px;
  color: var(--td-text-color-placeholder);
}

.model-card__vision {
  display: inline-flex;
  align-items: center;
  gap: 3px;
}

.model-card__actions {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  gap: 2px;
}

.model-card__action-btn {
  flex-shrink: 0;
  padding: 2px;
  opacity: 0;
  transition: opacity 0.15s ease;
}

.model-card__more {
  color: var(--td-text-color-placeholder);

  &:hover,
  &:focus-visible {
    background: var(--td-bg-color-secondarycontainer);
    color: var(--td-text-color-primary);
  }
}

// Hover / 键盘焦点 时显示操作按钮，避免静态卡片上有"杂物"。
.model-card:hover .model-card__action-btn,
.model-card:focus-within .model-card__action-btn,
.model-card__actions:focus-within .model-card__action-btn {
  opacity: 1;
}

.empty-state {
  padding: 64px 0;
  text-align: center;

  :deep(.t-empty__description) {
    font-size: 14px;
    color: var(--td-text-color-placeholder);
    margin-bottom: 16px;
  }
}

// 模型用量与成本概览（M2 成本可观测）
.usage-panel {
  margin-bottom: 24px;
  padding: 16px 20px;
  background: var(--td-bg-color-container);
  border: 1px solid var(--td-component-stroke);
  border-radius: 10px;
}

.usage-panel__header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 12px;
}

.usage-panel__title {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
  color: var(--td-text-color-primary);
}

.usage-panel__subtitle {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--td-text-color-secondary);
  line-height: 1.5;
}

.usage-panel__actions {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-shrink: 0;
}

.usage-panel__date {
  width: 240px;
}

.usage-panel__error {
  margin: 0;
  font-size: 13px;
  color: var(--td-error-color);
}

.usage-panel__empty {
  padding: 24px 0;
}

.usage-table {
  :deep(.t-table__header th) {
    font-size: 12px;
    color: var(--td-text-color-secondary);
  }

  :deep(.t-table__body td) {
    font-size: 13px;
  }
}
</style>
