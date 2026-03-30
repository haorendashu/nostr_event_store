package aggregation

import (
	"context"

	"github.com/haorendashu/nostr_event_store/src/index"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// Engine is the top-level aggregation API.
type Engine interface {
	// Aggregate compiles and executes an aggregation query.
	Aggregate(ctx context.Context, q *types.AggregationQuery) ([]types.AggregationEntry, error)

	// Explain compiles the query and returns a human-readable execution plan
	// without actually scanning any data.
	Explain(ctx context.Context, q *types.AggregationQuery) (string, error)
}

type engineImpl struct {
	compiler Compiler
	executor Executor
}

// NewEngine creates an aggregation Engine backed by the given index manager.
func NewEngine(indexMgr index.Manager) Engine {
	return &engineImpl{
		compiler: NewCompiler(indexMgr),
		executor: NewExecutor(indexMgr),
	}
}

// NewEngineWithKinds creates an aggregation Engine with a dynamic kinds provider.
// The knownKindsFunc is called at compile time to supply per-kind key ranges
// when the query does not specify kinds explicitly.
func NewEngineWithKinds(indexMgr index.Manager, knownKindsFunc func() []uint16) Engine {
	return &engineImpl{
		compiler: NewCompilerWithKinds(indexMgr, knownKindsFunc),
		executor: NewExecutor(indexMgr),
	}
}

func (e *engineImpl) Aggregate(ctx context.Context, q *types.AggregationQuery) ([]types.AggregationEntry, error) {
	plan, err := e.compiler.Compile(q)
	if err != nil {
		return nil, err
	}
	return e.executor.Execute(ctx, plan)
}

func (e *engineImpl) Explain(ctx context.Context, q *types.AggregationQuery) (string, error) {
	plan, err := e.compiler.Compile(q)
	if err != nil {
		return "", err
	}
	return plan.String(), nil
}
