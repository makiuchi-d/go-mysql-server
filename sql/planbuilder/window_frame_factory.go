package planbuilder

import (
	ast "github.com/dolthub/vitess/go/vt/sqlparser"

	"github.com/dolthub/go-mysql-server/sql"
)

//go:generate go run ../../optgen/cmd/optgen/main.go -out window_frame_factory.og.go -pkg planbuilder frameFactory window_frame_factory.go

func (b *PlanBuilder) getFrameStartNPreceding(inScope *scope, frame *ast.Frame) sql.Expression {
	if frame == nil || frame.Extent.Start.Type != ast.ExprPreceding {
		return nil
	}
	return b.buildScalar(inScope, frame.Extent.Start.Expr)
}

func (b *PlanBuilder) getFrameEndNPreceding(inScope *scope, frame *ast.Frame) sql.Expression {
	if frame == nil || frame.Extent.End == nil || frame.Extent.End.Type != ast.ExprPreceding {
		return nil
	}
	return b.buildScalar(inScope, frame.Extent.End.Expr)
}

func (b *PlanBuilder) getFrameStartNFollowing(inScope *scope, frame *ast.Frame) sql.Expression {
	if frame == nil || frame.Extent.Start.Type != ast.ExprFollowing {
		return nil
	}
	return b.buildScalar(inScope, frame.Extent.Start.Expr)
}

func (b *PlanBuilder) getFrameEndNFollowing(inScope *scope, frame *ast.Frame) sql.Expression {
	if frame == nil || frame.Extent.End == nil || frame.Extent.End.Type != ast.ExprFollowing {
		return nil
	}
	return b.buildScalar(inScope, frame.Extent.End.Expr)
}

func (b *PlanBuilder) getFrameStartCurrentRow(_ *scope, frame *ast.Frame) bool {
	return frame != nil && frame.Extent.Start.Type == ast.CurrentRow
}

func (b *PlanBuilder) getFrameEndCurrentRow(_ *scope, frame *ast.Frame) bool {
	if frame == nil {
		return false
	}
	if frame.Extent.End == nil {
		// if a frame is not null and only specifies start, default to current row
		return true
	}
	return frame != nil && frame.Extent.End != nil && frame.Extent.End.Type == ast.CurrentRow
}

func (b *PlanBuilder) getFrameUnboundedPreceding(_ *scope, frame *ast.Frame) bool {
	return frame != nil &&
		frame.Extent.Start != nil && frame.Extent.Start.Type == ast.UnboundedPreceding ||
		frame.Extent.End != nil && frame.Extent.End.Type == ast.UnboundedPreceding
}

func (b *PlanBuilder) getFrameUnboundedFollowing(_ *scope, frame *ast.Frame) bool {
	return frame != nil &&
		frame.Extent.Start != nil && frame.Extent.Start.Type == ast.UnboundedFollowing ||
		frame.Extent.End != nil && frame.Extent.End.Type == ast.UnboundedFollowing
}
