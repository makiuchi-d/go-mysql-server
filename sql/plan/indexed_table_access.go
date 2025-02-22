// Copyright 2020-2021 Dolthub, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package plan

import (
	"fmt"
	"strings"

	"gopkg.in/src-d/go-errors.v1"

	"github.com/dolthub/go-mysql-server/sql"
)

var ErrNoIndexableTable = errors.NewKind("expected an IndexableTable, couldn't find one in %v")
var ErrNoIndexedTableAccess = errors.NewKind("expected an IndexedTableAccess, couldn't find one in %v")
var ErrInvalidLookupForIndexedTable = errors.NewKind("indexable table does not support given lookup: %s")

// IndexedTableAccess represents an indexed lookup of a particular ResolvedTable. The values for the key used to access
// the indexed table is provided in RowIter(), or during static analysis.
type IndexedTableAccess struct {
	ResolvedTable *ResolvedTable
	lb            *LookupBuilder
	lookup        sql.IndexLookup
	Table         sql.IndexedTable
}

var _ sql.Table = (*IndexedTableAccess)(nil)
var _ sql.Node = (*IndexedTableAccess)(nil)
var _ sql.Nameable = (*IndexedTableAccess)(nil)
var _ sql.Expressioner = (*IndexedTableAccess)(nil)
var _ sql.CollationCoercible = (*IndexedTableAccess)(nil)

// NewIndexedTableAccess returns a new IndexedTableAccess node that will use
// the LookupBuilder to build lookups. An index lookup will be calculated and
// applied for the row given in RowIter().
func NewIndexedTableAccess(rt *ResolvedTable, t sql.IndexedTable, lb *LookupBuilder) *IndexedTableAccess {
	return &IndexedTableAccess{
		ResolvedTable: rt,
		lb:            lb,
		Table:         t,
	}
}

// NewIndexedAccessForResolvedTable creates an IndexedTableAccess node if the resolved table embeds
// an IndexAddressableTable, otherwise returns an error.
func NewIndexedAccessForResolvedTable(rt *ResolvedTable, lb *LookupBuilder) (*IndexedTableAccess, error) {
	var table = rt.Table
	if t, ok := table.(sql.TableWrapper); ok {
		table = t.Underlying()
	}
	iaTable, ok := table.(sql.IndexAddressableTable)
	if !ok {
		return nil, fmt.Errorf("table is not index addressable: %s", table.Name())
	}

	lookup, err := lb.GetLookup(lb.GetZeroKey())
	if err != nil {
		return nil, err
	}
	if !lookup.Index.CanSupport(lookup.Ranges...) {
		return nil, ErrInvalidLookupForIndexedTable.New(lookup.Ranges.DebugString())
	}
	ia := iaTable.IndexedAccess(lookup)
	return &IndexedTableAccess{
		ResolvedTable: rt,
		lb:            lb,
		Table:         ia,
	}, nil
}

// NewStaticIndexedTableAccess returns a new IndexedTableAccess node with the indexlookup given. It will be applied in
// RowIter() without consideration of the row given. The key expression should faithfully represent this lookup, but is
// only for display purposes.
func NewStaticIndexedTableAccess(rt *ResolvedTable, t sql.IndexedTable, lookup sql.IndexLookup) *IndexedTableAccess {
	return &IndexedTableAccess{
		ResolvedTable: rt,
		lookup:        lookup,
		Table:         t,
	}
}

// NewStaticIndexedAccessForResolvedTable creates an IndexedTableAccess node if the resolved table embeds
// an IndexAddressableTable, otherwise returns an error.
func NewStaticIndexedAccessForResolvedTable(rt *ResolvedTable, lookup sql.IndexLookup) (*IndexedTableAccess, error) {
	var table = rt.Table
	if t, ok := table.(sql.TableWrapper); ok {
		table = t.Underlying()
	}
	iaTable, ok := table.(sql.IndexAddressableTable)
	if !ok {
		return nil, fmt.Errorf("table is not index addressable: %s", table.Name())
	}

	if !lookup.Index.CanSupport(lookup.Ranges...) {
		return nil, ErrInvalidLookupForIndexedTable.New(lookup.Ranges.DebugString())
	}
	ia := iaTable.IndexedAccess(lookup)
	return &IndexedTableAccess{
		ResolvedTable: rt,
		lookup:        lookup,
		Table:         ia,
	}, nil
}

func (i *IndexedTableAccess) IsStatic() bool {
	return !i.lookup.IsEmpty()
}

func (i *IndexedTableAccess) Resolved() bool {
	return i.ResolvedTable.Resolved()
}

func (i *IndexedTableAccess) Schema() sql.Schema {
	return i.ResolvedTable.Schema()
}

func (i *IndexedTableAccess) Collation() sql.CollationID {
	return i.ResolvedTable.Collation()
}

func (i *IndexedTableAccess) Children() []sql.Node {
	return nil
}

func (i *IndexedTableAccess) WithChildren(children ...sql.Node) (sql.Node, error) {
	if len(children) != 0 {
		return nil, sql.ErrInvalidChildrenNumber.New(i, len(children), 0)
	}

	return i, nil
}

func (i *IndexedTableAccess) Name() string {
	return i.ResolvedTable.Name()
}

func (i *IndexedTableAccess) Database() sql.Database {
	return i.ResolvedTable.Database
}

func (i *IndexedTableAccess) CheckPrivileges(ctx *sql.Context, opChecker sql.PrivilegedOperationChecker) bool {
	return i.ResolvedTable.CheckPrivileges(ctx, opChecker)
}

// CollationCoercibility implements the interface sql.CollationCoercible.
func (i *IndexedTableAccess) CollationCoercibility(ctx *sql.Context) (collation sql.CollationID, coercibility byte) {
	return i.ResolvedTable.CollationCoercibility(ctx)
}

func (i *IndexedTableAccess) Index() sql.Index {
	if !i.lookup.IsEmpty() {
		return i.lookup.Index
	}
	return i.lb.index
}

// CanBuildIndex returns whether an index lookup on this table can be successfully built for a zero-valued key. For a
// static lookup, no lookup needs to be built, so returns true.
func (i *IndexedTableAccess) CanBuildIndex(ctx *sql.Context) (bool, error) {
	// If the lookup was provided at analysis time (static evaluation), then an index was already built
	if !i.lookup.IsEmpty() {
		return true, nil
	}

	key := i.lb.GetZeroKey()
	lookup, err := i.lb.GetLookup(key)
	return err == nil && !lookup.IsEmpty(), nil
}

func (i *IndexedTableAccess) GetLookup(ctx *sql.Context, row sql.Row) (sql.IndexLookup, error) {
	// if the lookup was provided at analysis time (static evaluation), use it.
	if !i.lookup.IsEmpty() {
		return i.lookup, nil
	}

	key, err := i.lb.GetKey(ctx, row)
	if err != nil {
		return sql.IndexLookup{}, err
	}
	return i.lb.GetLookup(key)
}

func (i *IndexedTableAccess) getLookup2(ctx *sql.Context, row sql.Row2) (sql.IndexLookup, error) {
	// if the lookup was provided at analysis time (static evaluation), use it.
	if !i.lookup.IsEmpty() {
		return i.lookup, nil
	}

	key, err := i.lb.GetKey2(ctx, row)
	if err != nil {
		return sql.IndexLookup{}, err
	}
	return i.lb.GetLookup(key)
}

func (i *IndexedTableAccess) String() string {
	pr := sql.NewTreePrinter()
	pr.WriteNode("IndexedTableAccess(%s)", i.ResolvedTable.Name())
	var children []string
	children = append(children, fmt.Sprintf("index: %s", formatIndexDecoratorString(i.Index())))
	if !i.lookup.IsEmpty() {
		children = append(children, fmt.Sprintf("filters: %s", i.lookup.Ranges.DebugString()))
	}

	if pt, ok := i.Table.(sql.ProjectedTable); ok {
		projections := pt.Projections()
		if projections != nil {
			columns := make([]string, len(projections))
			for i, c := range projections {
				columns[i] = strings.ToLower(c)
			}
			children = append(children, fmt.Sprintf("columns: %v", columns))
		}
	}

	if ft, ok := i.Table.(sql.FilteredTable); ok {
		var filters []string
		for _, f := range ft.Filters() {
			filters = append(filters, f.String())
		}
		if len(filters) > 0 {
			pr.WriteChildren(fmt.Sprintf("filters: %v", filters))
		}
	}

	if i.lookup.IsReverse {
		children = append(children, fmt.Sprintf("reverse: %v", i.lookup.IsReverse))
	}

	pr.WriteChildren(children...)
	return pr.String()
}

func formatIndexDecoratorString(idx sql.Index) string {
	var expStrs []string
	expStrs = append(expStrs, idx.Expressions()...)
	return fmt.Sprintf("[%s]", strings.Join(expStrs, ","))
}

func (i *IndexedTableAccess) DebugString() string {
	pr := sql.NewTreePrinter()
	pr.WriteNode("IndexedTableAccess(%s)", i.ResolvedTable.Name())
	var children []string
	children = append(children, fmt.Sprintf("index: %s", formatIndexDecoratorString(i.Index())))
	if !i.lookup.IsEmpty() {
		children = append(children, fmt.Sprintf("static: %s", i.lookup.Ranges.DebugString()))
	}

	var columns []string
	if pt, ok := i.Table.(sql.ProjectedTable); ok && pt.Projections() != nil {
		projections := pt.Projections()
		columns = make([]string, len(projections))
		for i, c := range projections {
			columns[i] = strings.ToLower(c)
		}
	} else {
		columns = make([]string, len(i.Table.Schema()))
		for i, c := range i.Table.Schema() {
			columns[i] = strings.ToLower(c.Name)
		}
	}
	children = append(children, fmt.Sprintf("columns: %v", columns))

	if ft, ok := i.Table.(sql.FilteredTable); ok {
		var filters []string
		for _, f := range ft.Filters() {
			filters = append(filters, f.String())
		}
		if len(filters) > 0 {
			pr.WriteChildren(fmt.Sprintf("filters: %v", filters))
		}
	}

	if i.lookup.IsReverse {
		children = append(children, fmt.Sprintf("reverse: %v", i.lookup.IsReverse))
	}

	pr.WriteChildren(children...)
	return pr.String()
}

// Expressions implements sql.Expressioner
func (i *IndexedTableAccess) Expressions() []sql.Expression {
	if !i.lookup.IsEmpty() {
		return nil
	}
	return i.lb.Expressions()
}

// WithExpressions implements sql.Expressioner
func (i *IndexedTableAccess) WithExpressions(exprs ...sql.Expression) (sql.Node, error) {
	if !i.lookup.IsEmpty() {
		if len(exprs) != 0 {
			return nil, sql.ErrInvalidChildrenNumber.New(i, len(exprs), 0)
		}
		n := *i
		return &n, nil
	}
	lb, err := i.lb.WithExpressions(i, exprs...)
	if err != nil {
		return nil, err
	}
	return &IndexedTableAccess{
		ResolvedTable: i.ResolvedTable,
		Table:         i.Table,
		lb:            lb,
	}, nil
}

// WithTable replaces the underlying ResolvedTable with the one given.
func (i IndexedTableAccess) WithTable(table sql.Table) (*IndexedTableAccess, error) {
	nrt, err := i.ResolvedTable.WithTable(table)
	if err != nil {
		return nil, err
	}
	i.ResolvedTable = nrt

	if t, ok := table.(sql.TableWrapper); ok {
		table = t.Underlying()
	}

	_, ok := table.(sql.IndexAddressableTable)
	if !ok {
		return nil, fmt.Errorf("table does not support indexed access")
	}

	var lookup sql.IndexLookup
	if i.lookup.Index != nil {
		lookup = i.lookup
	} else if i.lb != nil {
		lookup, err = i.lb.GetLookup(i.lb.GetZeroKey())
		if err != nil {
			return nil, err
		}
	}
	if lookup.Index.CanSupport(lookup.Ranges...) {
		return nil, ErrInvalidLookupForIndexedTable.New(sql.DebugString(i.lookup.Ranges))
	}

	return &i, nil
}

// Partitions implements sql.Table
func (i *IndexedTableAccess) Partitions(ctx *sql.Context) (sql.PartitionIter, error) {
	return i.Table.LookupPartitions(ctx, i.lookup)
}

// PartitionRows implements sql.Table
func (i *IndexedTableAccess) PartitionRows(ctx *sql.Context, partition sql.Partition) (sql.RowIter, error) {
	return i.Table.PartitionRows(ctx, partition)
}

// GetIndexLookup returns the sql.IndexLookup from an IndexedTableAccess.
// This method is exported for use in integration tests.
func GetIndexLookup(ita *IndexedTableAccess) sql.IndexLookup {
	return ita.lookup
}

type lookupBuilderKey []interface{}

// LookupBuilder abstracts secondary table access for an LookupJoin.
// A row from the primary table is first evaluated on the secondary index's
// expressions (columns) to produce a lookupBuilderKey. Consider the
// query below, assuming B has an index `xy (x,y)`:
//
// select * from A join B on a.x = b.x AND a.y = b.y
//
// Assume we choose A as the primary row source and B as a secondary lookup
// on `xy`. For every row in A, we will produce a lookupBuilderKey on B
// using the join condition. For the A row (x=1,y=2), the lookup key into B
// will be (1,2) to reflect the B-xy index access.
//
// Then we construct a sql.RangeCollection to represent the (1,2) point
// lookup into B-xy. The collection will always be a single range, because
// a point lookup cannot be a disjoint set of ranges. The range will also
// have the same dimension as the index itself. If the join condition is
// a partial prefix on the index (ex: INDEX x (x)), the unfiltered columns
// are padded.
//
// The <=> filter is a special case for two reasons. 1) It is not a point
// lookup, the corresponding range will either be IsNull or IsNotNull
// depending on whether the primary row key column is nil or not,
// respectfully. 2) The format of the output range is variable, while
// equality ranges are identical except for bound values.
//
// Currently the analyzer constructs one of these and uses it for the
// IndexedTableAccess nodes below an indexed join, for example. This struct is
// also used to implement Expressioner on the IndexedTableAccess node.
type LookupBuilder struct {
	keyExprs  []sql.Expression
	keyExprs2 []sql.Expression2

	// When building the lookup, we will use an IndexBuilder. If the
	// extracted lookup value is NULL, but we have a non-NULL safe
	// comparison, then the lookup should return no values. But if the
	// comparison is NULL-safe, then the lookup should returns indexed
	// values having that value <=> NULL. For each |keyExpr|, this field
	// contains |true| if the lookup should also match NULLs, and |false|
	// otherwise.
	matchesNullMask []bool

	index sql.Index

	key           lookupBuilderKey
	rang          sql.Range
	nullSafe      bool
	isPointLookup bool
	emptyRange    bool
	cets          []sql.ColumnExpressionType
}

func NewLookupBuilder(index sql.Index, keyExprs []sql.Expression, matchesNullMask []bool) *LookupBuilder {
	cets := index.ColumnExpressionTypes()
	var nullSafe = true
	for i := range matchesNullMask {
		if matchesNullMask[i] {
			nullSafe = false
		}
	}
	return &LookupBuilder{
		index:           index,
		keyExprs:        keyExprs,
		matchesNullMask: matchesNullMask,
		cets:            cets,
		nullSafe:        nullSafe,
		isPointLookup:   true,
	}
}

func (lb *LookupBuilder) initializeRange(key lookupBuilderKey) {
	lb.rang = make(sql.Range, len(lb.cets))
	lb.emptyRange = false
	lb.isPointLookup = len(key) == len(lb.cets)
	var i int
	for i < len(key) {
		if key[i] == nil {
			lb.emptyRange = true
			lb.isPointLookup = false
		}
		if lb.matchesNullMask[i] {
			if key[i] == nil {
				lb.rang[i] = sql.NullRangeColumnExpr(lb.cets[i].Type)

			} else {
				lb.rang[i] = sql.NotNullRangeColumnExpr(lb.cets[i].Type)
			}
		} else {
			lb.rang[i] = sql.ClosedRangeColumnExpr(key[i], key[i], lb.cets[i].Type)
		}
		i++
	}
	for i < len(lb.cets) {
		lb.rang[i] = sql.AllRangeColumnExpr(lb.cets[i].Type)
		lb.isPointLookup = false
		i++
	}
	return
}

func (lb *LookupBuilder) GetLookup(key lookupBuilderKey) (sql.IndexLookup, error) {
	if lb.rang == nil {
		lb.initializeRange(key)
		return sql.IndexLookup{
			Index:           lb.index,
			Ranges:          []sql.Range{lb.rang},
			IsPointLookup:   lb.nullSafe && lb.isPointLookup && lb.index.IsUnique(),
			IsEmptyRange:    lb.emptyRange,
			IsSpatialLookup: false,
		}, nil
	}

	lb.emptyRange = false
	lb.isPointLookup = len(key) == len(lb.cets)
	for i := range key {
		if key[i] == nil {
			lb.emptyRange = true
			lb.isPointLookup = false
		}
		if lb.matchesNullMask[i] {
			if key[i] == nil {
				lb.rang[i] = sql.NullRangeColumnExpr(lb.cets[i].Type)

			} else {
				lb.rang[i] = sql.NotNullRangeColumnExpr(lb.cets[i].Type)
			}
		} else {
			lb.rang[i].LowerBound = sql.Below{Key: key[i]}
			lb.rang[i].UpperBound = sql.Above{Key: key[i]}
		}
	}

	return sql.IndexLookup{
		Index:           lb.index,
		Ranges:          []sql.Range{lb.rang},
		IsPointLookup:   lb.nullSafe && lb.isPointLookup && lb.index.IsUnique(),
		IsEmptyRange:    lb.emptyRange,
		IsSpatialLookup: false,
	}, nil
}

func (lb *LookupBuilder) GetKey(ctx *sql.Context, row sql.Row) (lookupBuilderKey, error) {
	if lb.key == nil {
		lb.key = make([]interface{}, len(lb.keyExprs))
	}
	for i := range lb.keyExprs {
		var err error
		lb.key[i], err = lb.keyExprs[i].Eval(ctx, row)
		if err != nil {
			return nil, err
		}
	}
	return lb.key, nil
}

func (lb *LookupBuilder) GetKey2(ctx *sql.Context, row sql.Row2) (lookupBuilderKey, error) {
	if lb.key == nil {
		lb.key = make([]interface{}, len(lb.keyExprs))
	}
	for i := range lb.keyExprs {
		var err error
		lb.key[i], err = lb.keyExprs2[i].Eval2(ctx, row)
		if err != nil {
			return nil, err
		}
	}
	return lb.key, nil
}

func (lb *LookupBuilder) GetZeroKey() lookupBuilderKey {
	key := make(lookupBuilderKey, len(lb.keyExprs))
	for i, keyExpr := range lb.keyExprs {
		key[i] = keyExpr.Type().Zero()
	}
	return key
}

func (lb *LookupBuilder) Index() sql.Index {
	return lb.index
}

func (lb *LookupBuilder) Expressions() []sql.Expression {
	return lb.keyExprs
}

func (lb *LookupBuilder) DebugString() string {
	keyExprs := make([]string, len(lb.keyExprs))
	for i := range lb.keyExprs {
		keyExprs[i] = sql.DebugString(lb.keyExprs[i])
	}
	return fmt.Sprintf("on %s, using fields %s", formatIndexDecoratorString(lb.Index()), strings.Join(keyExprs, ", "))
}

func (lb *LookupBuilder) WithExpressions(node sql.Node, exprs ...sql.Expression) (*LookupBuilder, error) {
	if len(exprs) != len(lb.keyExprs) {
		return &LookupBuilder{}, sql.ErrInvalidChildrenNumber.New(node, len(exprs), len(lb.keyExprs))
	}
	return &LookupBuilder{
		keyExprs:        exprs,
		index:           lb.index,
		matchesNullMask: lb.matchesNullMask,
		rang:            lb.rang,
		cets:            lb.cets,
	}, nil
}
