package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/Tencent/WeKnora/internal/utils"
	"golang.org/x/sync/errgroup"
)

/*
corpus: pid -> content
queries: qid -> content
answers: aid -> content
qrels: qid -> pid
arels: qid -> aid
*/

// EvaluationService handles evaluation tasks for knowledge base and chat models
type EvaluationService struct {
	config               *config.Config                  // Application configuration
	dataset              interfaces.DatasetService       // Service for dataset operations
	knowledgeBaseService interfaces.KnowledgeBaseService // Service for knowledge base operations
	knowledgeService     interfaces.KnowledgeService     // Service for knowledge operations
	sessionService       interfaces.SessionService       // Service for chat sessions
	modelService         interfaces.ModelService         // Service for model operations
	evaluationRepository interfaces.EvaluationRepository // Persistent storage for evaluation tasks

	evaluationMemoryStorage *evaluationMemoryStorage // In-memory storage for evaluation tasks
}

func NewEvaluationService(
	config *config.Config,
	dataset interfaces.DatasetService,
	knowledgeBaseService interfaces.KnowledgeBaseService,
	knowledgeService interfaces.KnowledgeService,
	sessionService interfaces.SessionService,
	modelService interfaces.ModelService,
	evaluationRepository interfaces.EvaluationRepository,
) interfaces.EvaluationService {
	evaluationMemoryStorage := newEvaluationMemoryStorage()
	return &EvaluationService{
		config:                  config,
		dataset:                 dataset,
		knowledgeBaseService:    knowledgeBaseService,
		knowledgeService:        knowledgeService,
		sessionService:          sessionService,
		modelService:            modelService,
		evaluationRepository:    evaluationRepository,
		evaluationMemoryStorage: evaluationMemoryStorage,
	}
}

// evaluationMemoryStorage stores evaluation tasks in memory with thread-safe access
type evaluationMemoryStorage struct {
	store map[string]*types.EvaluationDetail // Map of taskID to evaluation details
	mu    *sync.RWMutex                      // Read-write lock for concurrent access
}

func newEvaluationMemoryStorage() *evaluationMemoryStorage {
	res := &evaluationMemoryStorage{
		store: make(map[string]*types.EvaluationDetail),
		mu:    &sync.RWMutex{},
	}
	return res
}

func (e *evaluationMemoryStorage) register(params *types.EvaluationDetail) {
	e.mu.Lock()
	defer e.mu.Unlock()
	logger.Infof(context.Background(), "Registering evaluation task: %s", params.Task.ID)
	e.store[params.Task.ID] = params
}

func (e *evaluationMemoryStorage) get(taskID string) (*types.EvaluationDetail, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	logger.Infof(context.Background(), "Getting evaluation task: %s", taskID)
	res, ok := e.store[taskID]
	if !ok {
		return nil, errors.New("task not found")
	}
	return res, nil
}

func (e *evaluationMemoryStorage) update(taskID string, fn func(params *types.EvaluationDetail)) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	params, ok := e.store[taskID]
	if !ok {
		return errors.New("task not found")
	}
	fn(params)
	return nil
}

func (e *EvaluationService) EvaluationResult(ctx context.Context, taskID string) (*types.EvaluationDetail, error) {
	logger.Info(ctx, "Start getting evaluation result")
	logger.Infof(ctx, "Task ID: %s", taskID)

	tenantID := types.MustTenantIDFromContext(ctx)

	// 先查内存（运行中的任务轮询走这条快路径），查不到再回退到数据库，
	// 这样服务重启后依然能拿到历史评测结果。
	detail, err := e.evaluationMemoryStorage.get(taskID)
	if err != nil {
		logger.Infof(ctx, "Task not in memory, falling back to database: %s", taskID)
		detail, err = e.evaluationResultFromDB(ctx, tenantID, taskID)
		if err != nil {
			logger.Errorf(ctx, "Failed to get evaluation task: %v", err)
			return nil, err
		}
		if detail == nil {
			return nil, errors.New("task not found")
		}
		return detail, nil
	}

	logger.Infof(
		ctx,
		"Checking tenant ID match, task tenant ID: %d, current tenant ID: %d",
		detail.Task.TenantID, tenantID,
	)

	if tenantID != detail.Task.TenantID {
		logger.Error(ctx, "Tenant ID mismatch")
		return nil, errors.New("tenant ID does not match")
	}

	logger.Info(ctx, "Evaluation result retrieved successfully")
	return detail, nil
}

// evaluationResultFromDB 从数据库读取评测任务并重建 EvaluationDetail。
func (e *EvaluationService) evaluationResultFromDB(
	ctx context.Context, tenantID uint64, taskID string,
) (*types.EvaluationDetail, error) {
	record, err := e.evaluationRepository.GetByID(ctx, tenantID, taskID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	return recordToDetail(record), nil
}

// Evaluation starts a new evaluation task with given parameters
// datasetID: ID of the dataset to evaluate against
// knowledgeBaseID: ID of the knowledge base to use (empty to create new)
// chatModelID: ID of the chat model to evaluate
// rerankModelID: ID of the rerank model to evaluate
func (e *EvaluationService) Evaluation(ctx context.Context,
	datasetID string, knowledgeBaseID string, chatModelID string, rerankModelID string,
) (*types.EvaluationDetail, error) {
	logger.Info(ctx, "Start evaluation")
	logger.Infof(ctx, "Dataset ID: %s, Knowledge Base ID: %s, Chat Model ID: %s, Rerank Model ID: %s",
		datasetID, knowledgeBaseID, chatModelID, rerankModelID)

	// Get tenant ID from context for multi-tenancy support
	tenantID := types.MustTenantIDFromContext(ctx)
	logger.Infof(ctx, "Tenant ID: %d", tenantID)

	// Handle knowledge base creation if not provided
	if knowledgeBaseID == "" {
		logger.Info(ctx, "No knowledge base ID provided, creating new knowledge base")
		// Create new knowledge base with default evaluation settings
		// 获取默认的嵌入模型和LLM模型
		models, err := e.modelService.ListModels(ctx)
		if err != nil {
			logger.Errorf(ctx, "Failed to list models: %v", err)
			return nil, err
		}

		var embeddingModelID, llmModelID string
		for _, model := range models {
			if model == nil {
				continue
			}
			if model.Type == types.ModelTypeEmbedding {
				embeddingModelID = model.ID
			}
			if model.Type == types.ModelTypeKnowledgeQA {
				llmModelID = model.ID
			}
		}

		if embeddingModelID == "" || llmModelID == "" {
			return nil, fmt.Errorf("no default models found for evaluation")
		}

		kb, err := e.knowledgeBaseService.CreateKnowledgeBase(ctx, &types.KnowledgeBase{
			Name:             "evaluation",
			Description:      "evaluation",
			EmbeddingModelID: embeddingModelID,
			SummaryModelID:   llmModelID,
		})
		if err != nil {
			logger.Errorf(ctx, "Failed to create knowledge base: %v", err)
			return nil, err
		}
		knowledgeBaseID = kb.ID
		logger.Infof(ctx, "Created new knowledge base with ID: %s", knowledgeBaseID)
	} else {
		logger.Infof(ctx, "Using existing knowledge base ID: %s", knowledgeBaseID)
		// Create evaluation-specific knowledge base based on existing one
		kb, err := e.knowledgeBaseService.GetKnowledgeBaseByID(ctx, knowledgeBaseID)
		if err != nil {
			logger.Errorf(ctx, "Failed to get knowledge base: %v", err)
			return nil, err
		}

		kb, err = e.knowledgeBaseService.CreateKnowledgeBase(ctx, &types.KnowledgeBase{
			Name:             "evaluation",
			Description:      "evaluation",
			EmbeddingModelID: kb.EmbeddingModelID,
			SummaryModelID:   kb.SummaryModelID,
		})
		if err != nil {
			logger.Errorf(ctx, "Failed to create knowledge base: %v", err)
			return nil, err
		}
		knowledgeBaseID = kb.ID
		logger.Infof(ctx, "Created new knowledge base with ID: %s based on existing one", knowledgeBaseID)
	}

	// Set default values for optional parameters
	if datasetID == "" {
		datasetID = "default"
		logger.Info(ctx, "Using default dataset")
	}

	if rerankModelID == "" {
		// 获取默认的重排模型
		models, err := e.modelService.ListModels(ctx)
		if err == nil {
			for _, model := range models {
				if model == nil {
					continue
				}
				if model.Type == types.ModelTypeRerank {
					rerankModelID = model.ID
					break
				}
			}
		}
		if rerankModelID == "" {
			logger.Warnf(ctx, "No rerank model found, skipping rerank")
		} else {
			logger.Infof(ctx, "Using default rerank model: %s", rerankModelID)
		}
	}

	if chatModelID == "" {
		// 获取默认的LLM模型
		models, err := e.modelService.ListModels(ctx)
		if err == nil {
			for _, model := range models {
				if model == nil {
					continue
				}
				if model.Type == types.ModelTypeKnowledgeQA {
					chatModelID = model.ID
					break
				}
			}
		}
		if chatModelID == "" {
			return nil, fmt.Errorf("no default chat model found")
		}
		logger.Infof(ctx, "Using default chat model: %s", chatModelID)
	}

	// Create evaluation task with unique ID
	logger.Info(ctx, "Creating evaluation task")
	taskID := utils.GenerateTaskID("evaluation", tenantID, datasetID)
	logger.Infof(ctx, "Generated task ID: %s", taskID)

	// Prepare evaluation detail with all parameters
	detail := &types.EvaluationDetail{
		Task: &types.EvaluationTask{
			ID:        taskID,
			TenantID:  tenantID,
			DatasetID: datasetID,
			Status:    types.EvaluationStatuePending,
			StartTime: time.Now(),
		},
		Cost: &types.EvaluationCost{},
		Params: &types.ChatManage{
			PipelineRequest: types.PipelineRequest{
				VectorThreshold:  e.config.Conversation.VectorThreshold,
				KeywordThreshold: e.config.Conversation.KeywordThreshold,
				EmbeddingTopK:    e.config.Conversation.EmbeddingTopK,
				MaxRounds:        e.config.Conversation.MaxRounds,
				RerankModelID:    rerankModelID,
				RerankTopK:       e.config.Conversation.RerankTopK,
				RerankThreshold:  e.config.Conversation.RerankThreshold,
				ChatModelID:      chatModelID,
				SummaryConfig: types.SummaryConfig{
					MaxTokens:           e.config.Conversation.Summary.MaxTokens,
					RepeatPenalty:       e.config.Conversation.Summary.RepeatPenalty,
					TopK:                e.config.Conversation.Summary.TopK,
					TopP:                e.config.Conversation.Summary.TopP,
					Prompt:              e.config.Conversation.Summary.Prompt,
					ContextTemplate:     e.config.Conversation.Summary.ContextTemplate,
					FrequencyPenalty:    e.config.Conversation.Summary.FrequencyPenalty,
					PresencePenalty:     e.config.Conversation.Summary.PresencePenalty,
					NoMatchPrefix:       e.config.Conversation.Summary.NoMatchPrefix,
					Temperature:         e.config.Conversation.Summary.Temperature,
					Seed:                e.config.Conversation.Summary.Seed,
					MaxCompletionTokens: e.config.Conversation.Summary.MaxCompletionTokens,
				},
				FallbackResponse:    e.config.Conversation.FallbackResponse,
				RewritePromptSystem: e.config.Conversation.RewritePromptSystem,
				RewritePromptUser:   e.config.Conversation.RewritePromptUser,
			},
		},
	}

	// Store evaluation task in memory storage
	logger.Info(ctx, "Registering evaluation task")
	e.evaluationMemoryStorage.register(detail)

	// Persist initial task record (status=Pending) so a crashed/restarted
	// service can still find the task and its config snapshot.
	if err := e.evaluationRepository.Create(ctx, detailToRecord(detail)); err != nil {
		logger.Errorf(ctx, "Failed to persist evaluation task %s: %v", taskID, err)
	}

	// Start evaluation in background goroutine
	logger.Info(ctx, "Starting evaluation in background")
	go func() {
		// Create new context with logger for background task
		newCtx := logger.CloneContext(ctx)
		logger.Infof(newCtx, "Background evaluation started for task ID: %s", taskID)

		// Update task status to running
		detail.Task.Status = types.EvaluationStatueRunning
		logger.Info(newCtx, "Evaluation task status set to running")
		e.persistRecord(newCtx, detail, nil)

		// Execute actual evaluation
		if err := e.EvalDataset(newCtx, detail, knowledgeBaseID); err != nil {
			detail.Task.Status = types.EvaluationStatueFailed
			detail.Task.ErrMsg = err.Error()
			logger.Errorf(newCtx, "Evaluation task failed: %v, task ID: %s", err, taskID)
			e.finalizeRecord(newCtx, detail)
			return
		}

		// Mark task as completed successfully
		logger.Infof(newCtx, "Evaluation task completed successfully, task ID: %s", taskID)
		detail.Task.Status = types.EvaluationStatueSuccess
		e.finalizeRecord(newCtx, detail)
	}()

	logger.Infof(ctx, "Evaluation task created successfully, task ID: %s", taskID)
	return detail, nil
}

// EvalDataset performs the actual evaluation of a dataset
// Processes each QA pair in parallel and records metrics
func (e *EvaluationService) EvalDataset(ctx context.Context, detail *types.EvaluationDetail, knowledgeBaseID string) error {
	logger.Info(ctx, "Start evaluating dataset")
	logger.Infof(ctx, "Task ID: %s, Dataset ID: %s", detail.Task.ID, detail.Task.DatasetID)

	// Ensure cost accumulator exists (defensive: Evaluation may not have initialized it).
	if detail.Cost == nil {
		detail.Cost = &types.EvaluationCost{}
	}

	// Retrieve dataset from storage
	dataset, err := e.dataset.GetDatasetByID(ctx, detail.Task.DatasetID)
	if err != nil {
		logger.Errorf(ctx, "Failed to get dataset: %v", err)
		return err
	}
	logger.Infof(ctx, "Dataset retrieved successfully with %d QA pairs", len(dataset))

	// Update total QA pairs count in task details
	e.evaluationMemoryStorage.update(detail.Task.ID, func(params *types.EvaluationDetail) {
		params.Task.Total = len(dataset)
		logger.Infof(ctx, "Updated task total to %d QA pairs", params.Task.Total)
	})

	// Extract and organize passages from dataset
	passages := getPassageList(dataset)
	logger.Infof(ctx, "Creating knowledge from %d passages", len(passages))

	// Create knowledge base from passages (sync: wait for indexing to complete before querying)
	knowledge, err := e.knowledgeService.CreateKnowledgeFromPassageSync(ctx, knowledgeBaseID, passages, "")
	if err != nil {
		logger.Errorf(ctx, "Failed to create knowledge from passages: %v", err)
		return err
	}
	logger.Infof(ctx, "Knowledge created and indexed successfully, ID: %s", knowledge.ID)

	// Setup cleanup of temporary resources
	defer func() {
		logger.Infof(ctx, "Cleaning up resources - deleting knowledge: %s", knowledge.ID)
		if err := e.knowledgeService.DeleteKnowledge(ctx, knowledge.ID); err != nil {
			logger.Errorf(ctx, "Failed to delete knowledge: %v, knowledge ID: %s", err, knowledge.ID)
		}

		logger.Infof(ctx, "Cleaning up resources - deleting knowledge base: %s", knowledgeBaseID)
		if err := e.knowledgeBaseService.DeleteKnowledgeBase(ctx, knowledgeBaseID); err != nil {
			logger.Errorf(
				ctx,
				"Failed to delete knowledge base: %v, knowledge base ID: %s",
				err, knowledgeBaseID,
			)
		}
	}()

	// Initialize parallel evaluation metrics
	var finished int
	var mu sync.Mutex
	var g errgroup.Group
	metricHook := NewHookMetric(len(dataset), passages)

	// Set worker limit based on available CPUs
	g.SetLimit(max(runtime.GOMAXPROCS(0)-1, 1))
	logger.Infof(ctx, "Starting evaluation with %d parallel workers", max(runtime.GOMAXPROCS(0)-1, 1))

	// Process each QA pair in parallel
	for i, qaPair := range dataset {
		qaPair := qaPair
		i := i
		g.Go(func() error {
			logger.Infof(ctx, "Processing QA pair %d, question: %s", i, qaPair.Question)

			// Prepare chat management parameters for this QA pair
			chatManage := detail.Params.Clone()
			chatManage.Query = qaPair.Question
			chatManage.RewriteQuery = qaPair.Question
			// Set knowledge base ID and search targets for this evaluation
			chatManage.KnowledgeBaseIDs = []string{knowledgeBaseID}
			chatManage.SearchTargets = types.SearchTargets{
				&types.SearchTarget{
					Type:            types.SearchTargetTypeKnowledgeBase,
					KnowledgeBaseID: knowledgeBaseID,
				},
			}

			// Execute knowledge QA pipeline
			logger.Infof(ctx, "Running knowledge QA for question: %s", qaPair.Question)
			err = e.sessionService.KnowledgeQAByEvent(ctx, chatManage, types.Pipline["rag"])
			if err != nil {
				logger.Errorf(ctx, "Failed to process question %d: %v", i, err)
				return err
			}

			// Record evaluation metrics
			logger.Infof(ctx, "Recording metrics for QA pair %d", i)
			metricHook.recordInit(i)
			metricHook.recordQaPair(i, qaPair)
			metricHook.recordSearchResult(i, chatManage.SearchResult)
			metricHook.recordRerankResult(i, chatManage.RerankResult)
			metricHook.recordChatResponse(i, chatManage.ChatResponse)
			metricHook.recordFinish(i)

			// Update progress metrics
			mu.Lock()
			finished += 1
			metricResult := metricHook.MetricResult()
			if chatManage.ChatResponse != nil {
				detail.Cost.PromptTokens += int64(chatManage.ChatResponse.Usage.PromptTokens)
				detail.Cost.CompletionTokens += int64(chatManage.ChatResponse.Usage.CompletionTokens)
				detail.Cost.TotalTokens += int64(chatManage.ChatResponse.Usage.TotalTokens)
			}
			mu.Unlock()
			e.evaluationMemoryStorage.update(detail.Task.ID, func(params *types.EvaluationDetail) {
				params.Metric = metricResult
				params.Task.Finished = finished
				logger.Infof(ctx, "Updated task progress: %d/%d completed", finished, params.Task.Total)
			})
			return nil
		})
	}

	// Wait for all parallel evaluations to complete
	logger.Info(ctx, "Waiting for all evaluation tasks to complete")
	if err := g.Wait(); err != nil {
		logger.Errorf(ctx, "Evaluation error: %v", err)
		return err
	}

	// Final update of evaluation metrics
	e.evaluationMemoryStorage.update(detail.Task.ID, func(params *types.EvaluationDetail) {
		params.Metric = metricHook.MetricResult()
		params.Task.Finished = finished
	})

	logger.Infof(ctx, "Dataset evaluation completed successfully, task ID: %s", detail.Task.ID)
	return nil
}

// getPassageList extracts and organizes passages from QA pairs
// Returns a slice of passages indexed by their passage IDs
func getPassageList(dataset []*types.QAPair) []string {
	pIDMap := make(map[int]string)
	maxPID := 0
	for _, qaPair := range dataset {
		for i := 0; i < len(qaPair.PIDs); i++ {
			pIDMap[qaPair.PIDs[i]] = qaPair.Passages[i]
			maxPID = max(maxPID, qaPair.PIDs[i])
		}
	}
	passages := make([]string, maxPID+1)
	for i := 0; i <= maxPID; i++ {
		if _, ok := pIDMap[i]; ok {
			passages[i] = pIDMap[i]
		}
	}
	return passages
}

// persistRecord 把内存中的 detail 落库（全量更新，含零值字段）。
// endTime 为 nil 表示任务尚未结束（如刚置为 Running）。
func (e *EvaluationService) persistRecord(ctx context.Context, detail *types.EvaluationDetail, endTime *time.Time) {
	record := detailToRecord(detail)
	record.EndTime = endTime
	if err := e.evaluationRepository.Update(ctx, record); err != nil {
		logger.Errorf(ctx, "Failed to persist evaluation task %s: %v", detail.Task.ID, err)
	}
}

// finalizeRecord 在任务结束时计算总耗时并落库。
func (e *EvaluationService) finalizeRecord(ctx context.Context, detail *types.EvaluationDetail) {
	now := time.Now()
	if detail.Cost != nil {
		detail.Cost.LatencyMs = now.Sub(detail.Task.StartTime).Milliseconds()
	}
	e.persistRecord(ctx, detail, &now)
}

// detailToRecord 把内存结构 EvaluationDetail 转换为持久化模型。
func detailToRecord(detail *types.EvaluationDetail) *types.EvaluationTaskRecord {
	record := &types.EvaluationTaskRecord{
		ID:        detail.Task.ID,
		TenantID:  detail.Task.TenantID,
		DatasetID: detail.Task.DatasetID,
		Status:    int(detail.Task.Status),
		ErrMsg:    detail.Task.ErrMsg,
		Total:     detail.Task.Total,
		Finished:  detail.Task.Finished,
		StartTime: detail.Task.StartTime,
	}
	if detail.Params != nil {
		if b, err := json.Marshal(detail.Params); err == nil {
			record.Params = types.JSON(b)
		}
	}
	if detail.Metric != nil {
		if b, err := json.Marshal(detail.Metric); err == nil {
			record.Metric = types.JSON(b)
		}
	}
	if detail.Cost != nil {
		record.PromptTokens = detail.Cost.PromptTokens
		record.CompletionTokens = detail.Cost.CompletionTokens
		record.TotalTokens = detail.Cost.TotalTokens
		record.LatencyMs = detail.Cost.LatencyMs
	}
	return record
}

// recordToDetail 把持久化模型还原为内存结构（用于服务重启后查询历史结果）。
func recordToDetail(record *types.EvaluationTaskRecord) *types.EvaluationDetail {
	detail := &types.EvaluationDetail{
		Task: &types.EvaluationTask{
			ID:        record.ID,
			TenantID:  record.TenantID,
			DatasetID: record.DatasetID,
			Status:    types.EvaluationStatue(record.Status),
			ErrMsg:    record.ErrMsg,
			Total:     record.Total,
			Finished:  record.Finished,
			StartTime: record.StartTime,
		},
		Cost: &types.EvaluationCost{
			PromptTokens:     record.PromptTokens,
			CompletionTokens: record.CompletionTokens,
			TotalTokens:      record.TotalTokens,
			LatencyMs:        record.LatencyMs,
		},
	}
	if len(record.Params) > 0 {
		var params types.ChatManage
		if err := json.Unmarshal(record.Params, &params); err == nil {
			detail.Params = &params
		}
	}
	if len(record.Metric) > 0 {
		var metric types.MetricResult
		if err := json.Unmarshal(record.Metric, &metric); err == nil {
			detail.Metric = &metric
		}
	}
	return detail
}
