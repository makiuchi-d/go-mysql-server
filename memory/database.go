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

package memory

import (
	"strings"

	"github.com/dolthub/go-mysql-server/sql/expression"
	"github.com/dolthub/go-mysql-server/sql/types"

	"github.com/dolthub/go-mysql-server/sql"
)

// Database is an in-memory database.
type Database struct {
	*BaseDatabase
	views map[string]sql.ViewDefinition
}

type MemoryDatabase interface {
	sql.Database
	AddTable(name string, t sql.Table)
}

var _ sql.Database = (*Database)(nil)
var _ sql.TableCreator = (*Database)(nil)
var _ sql.IndexedTableCreator = (*Database)(nil)
var _ sql.TableDropper = (*Database)(nil)
var _ sql.TableRenamer = (*Database)(nil)
var _ sql.TriggerDatabase = (*Database)(nil)
var _ sql.StoredProcedureDatabase = (*Database)(nil)
var _ sql.EventDatabase = (*Database)(nil)
var _ sql.ViewDatabase = (*Database)(nil)
var _ sql.CollatedDatabase = (*Database)(nil)

// BaseDatabase is an in-memory database that can't store views, only for testing the engine
type BaseDatabase struct {
	name              string
	tables            map[string]sql.Table
	fkColl            *ForeignKeyCollection
	triggers          []sql.TriggerDefinition
	storedProcedures  []sql.StoredProcedureDetails
	events            []sql.EventDefinition
	primaryKeyIndexes bool
	collation         sql.CollationID
}

var _ MemoryDatabase = (*Database)(nil)
var _ MemoryDatabase = (*BaseDatabase)(nil)

// NewDatabase creates a new database with the given name.
func NewDatabase(name string) *Database {
	return &Database{
		BaseDatabase: NewViewlessDatabase(name),
		views:        make(map[string]sql.ViewDefinition),
	}
}

// NewViewlessDatabase creates a new database that doesn't persist views. Used only for testing. Use NewDatabase.
func NewViewlessDatabase(name string) *BaseDatabase {
	return &BaseDatabase{
		name:   name,
		tables: map[string]sql.Table{},
		fkColl: newForeignKeyCollection(),
	}
}

// EnablePrimaryKeyIndexes causes every table created in this database to use an index on its primary partitionKeys
func (d *BaseDatabase) EnablePrimaryKeyIndexes() {
	d.primaryKeyIndexes = true
}

// Name returns the database name.
func (d *BaseDatabase) Name() string {
	return d.name
}

// Tables returns all tables in the database.
func (d *BaseDatabase) Tables() map[string]sql.Table {
	return d.tables
}

func (d *BaseDatabase) GetTableInsensitive(ctx *sql.Context, tblName string) (sql.Table, bool, error) {
	tbl, ok := sql.GetTableInsensitive(tblName, d.tables)
	return tbl, ok, nil
}

func (d *BaseDatabase) GetTableNames(ctx *sql.Context) ([]string, error) {
	tblNames := make([]string, 0, len(d.tables))
	for k := range d.tables {
		tblNames = append(tblNames, k)
	}

	return tblNames, nil
}

func (d *BaseDatabase) GetForeignKeyCollection() *ForeignKeyCollection {
	return d.fkColl
}

// HistoryDatabase is a test-only VersionedDatabase implementation. It only supports exact lookups, not AS OF queries
// between two revisions. It's constructed just like its non-versioned sibling, but it can receive updates to particular
// tables via the AddTableAsOf method. Consecutive calls to AddTableAsOf with the same table must install new versions
// of the named table each time, with ascending version identifiers, for this to work.
type HistoryDatabase struct {
	*Database
	Revisions    map[string]map[interface{}]sql.Table
	currRevision interface{}
}

var _ sql.VersionedDatabase = (*HistoryDatabase)(nil)

func (db *HistoryDatabase) GetTableInsensitiveAsOf(ctx *sql.Context, tblName string, time interface{}) (sql.Table, bool, error) {
	table, ok := db.Revisions[strings.ToLower(tblName)][time]
	if ok {
		return table, true, nil
	}

	// If we have revisions for the named table, but not the named revision, consider it not found.
	if _, ok := db.Revisions[strings.ToLower(tblName)]; ok {
		return nil, false, sql.ErrTableNotFound.New(tblName)
	}

	// Otherwise (this table has no revisions), return it as an unversioned lookup
	return db.GetTableInsensitive(ctx, tblName)
}

func (db *HistoryDatabase) GetTableNamesAsOf(ctx *sql.Context, time interface{}) ([]string, error) {
	// TODO: this can't make any queries fail (only used for error messages on table lookup failure), but would be nice
	//  to support better.
	return db.GetTableNames(ctx)
}

func NewHistoryDatabase(name string) *HistoryDatabase {
	return &HistoryDatabase{
		Database:  NewDatabase(name),
		Revisions: make(map[string]map[interface{}]sql.Table),
	}
}

// Adds a table with an asOf revision key. The table given becomes the current version for the name given.
func (db *HistoryDatabase) AddTableAsOf(name string, t sql.Table, asOf interface{}) {
	// TODO: this won't handle table names that vary only in case
	if _, ok := db.Revisions[strings.ToLower(name)]; !ok {
		db.Revisions[strings.ToLower(name)] = make(map[interface{}]sql.Table)
	}

	db.Revisions[strings.ToLower(name)][asOf] = t
	db.tables[name] = t
}

// AddTable adds a new table to the database.
func (d *BaseDatabase) AddTable(name string, t sql.Table) {
	d.tables[name] = t
}

// CreateTable creates a table with the given name and schema
func (d *BaseDatabase) CreateTable(ctx *sql.Context, name string, schema sql.PrimaryKeySchema, collation sql.CollationID) error {
	_, ok := d.tables[name]
	if ok {
		return sql.ErrTableAlreadyExists.New(name)
	}

	table := NewTableWithCollation(name, schema, d.fkColl, collation)
	if d.primaryKeyIndexes {
		table.EnablePrimaryKeyIndexes()
	}
	d.tables[name] = table
	return nil
}

// CreateIndexedTable creates a table with the given name and schema
func (d *BaseDatabase) CreateIndexedTable(ctx *sql.Context, name string, sch sql.PrimaryKeySchema, idxDef sql.IndexDef, collation sql.CollationID) error {
	_, ok := d.tables[name]
	if ok {
		return sql.ErrTableAlreadyExists.New(name)
	}

	table := NewTableWithCollation(name, sch, d.fkColl, collation)
	if d.primaryKeyIndexes {
		table.EnablePrimaryKeyIndexes()
	}

	for _, idxCol := range idxDef.Columns {
		idx := sch.Schema.IndexOfColName(idxCol.Name)
		if idx == -1 {
			return sql.ErrColumnNotFound.New(idxCol.Name)
		}
		col := sch.Schema[idx]
		if col.PrimaryKey && types.IsText(col.Type) && idxCol.Length > 0 {
			return sql.ErrUnsupportedIndexPrefix.New(col.Name)
		}
	}

	d.tables[name] = table
	return nil
}

// DropTable drops the table with the given name
func (d *BaseDatabase) DropTable(ctx *sql.Context, name string) error {
	_, ok := d.tables[name]
	if !ok {
		return sql.ErrTableNotFound.New(name)
	}

	delete(d.tables, name)
	return nil
}

func (d *BaseDatabase) RenameTable(ctx *sql.Context, oldName, newName string) error {
	tbl, ok := d.tables[oldName]
	if !ok {
		// Should be impossible (engine already checks this condition)
		return sql.ErrTableNotFound.New(oldName)
	}

	_, ok = d.tables[newName]
	if ok {
		return sql.ErrTableAlreadyExists.New(newName)
	}

	memTbl := tbl.(*Table)
	memTbl.name = newName
	for _, col := range memTbl.schema.Schema {
		col.Source = newName
	}
	for _, index := range memTbl.indexes {
		memIndex := index.(*Index)
		for i, expr := range memIndex.Exprs {
			getField := expr.(*expression.GetField)
			memIndex.Exprs[i] = expression.NewGetFieldWithTable(i, getField.Type(), newName, getField.Name(), getField.IsNullable())
		}
	}
	d.tables[newName] = tbl
	delete(d.tables, oldName)

	return nil
}

func (d *BaseDatabase) GetTriggers(ctx *sql.Context) ([]sql.TriggerDefinition, error) {
	var triggers []sql.TriggerDefinition
	triggers = append(triggers, d.triggers...)
	return triggers, nil
}

func (d *BaseDatabase) CreateTrigger(ctx *sql.Context, definition sql.TriggerDefinition) error {
	d.triggers = append(d.triggers, definition)
	return nil
}

func (d *BaseDatabase) DropTrigger(ctx *sql.Context, name string) error {
	found := false
	for i, trigger := range d.triggers {
		if trigger.Name == name {
			d.triggers = append(d.triggers[:i], d.triggers[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		return sql.ErrTriggerDoesNotExist.New(name)
	}
	return nil
}

// GetStoredProcedure implements sql.StoredProcedureDatabase
func (d *BaseDatabase) GetStoredProcedure(ctx *sql.Context, name string) (sql.StoredProcedureDetails, bool, error) {
	name = strings.ToLower(name)
	for _, spd := range d.storedProcedures {
		if name == strings.ToLower(spd.Name) {
			return spd, true, nil
		}
	}
	return sql.StoredProcedureDetails{}, false, nil
}

// GetStoredProcedures implements sql.StoredProcedureDatabase
func (d *BaseDatabase) GetStoredProcedures(ctx *sql.Context) ([]sql.StoredProcedureDetails, error) {
	var spds []sql.StoredProcedureDetails
	spds = append(spds, d.storedProcedures...)
	return spds, nil
}

// SaveStoredProcedure implements sql.StoredProcedureDatabase
func (d *BaseDatabase) SaveStoredProcedure(ctx *sql.Context, spd sql.StoredProcedureDetails) error {
	loweredName := strings.ToLower(spd.Name)
	for _, existingSpd := range d.storedProcedures {
		if strings.ToLower(existingSpd.Name) == loweredName {
			return sql.ErrStoredProcedureAlreadyExists.New(spd.Name)
		}
	}
	d.storedProcedures = append(d.storedProcedures, spd)
	return nil
}

// DropStoredProcedure implements sql.StoredProcedureDatabase
func (d *BaseDatabase) DropStoredProcedure(ctx *sql.Context, name string) error {
	loweredName := strings.ToLower(name)
	found := false
	for i, spd := range d.storedProcedures {
		if strings.ToLower(spd.Name) == loweredName {
			d.storedProcedures = append(d.storedProcedures[:i], d.storedProcedures[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		return sql.ErrStoredProcedureDoesNotExist.New(name)
	}
	return nil
}

// GetEvent implements sql.EventDatabase
func (d *BaseDatabase) GetEvent(ctx *sql.Context, name string) (sql.EventDefinition, bool, error) {
	name = strings.ToLower(name)
	for _, ed := range d.events {
		if name == strings.ToLower(ed.Name) {
			return ed, true, nil
		}
	}
	return sql.EventDefinition{}, false, nil
}

// GetEvents implements sql.EventDatabase
func (d *BaseDatabase) GetEvents(ctx *sql.Context) ([]sql.EventDefinition, error) {
	var eds []sql.EventDefinition
	eds = append(eds, d.events...)
	return eds, nil
}

// SaveEvent implements sql.EventDatabase
func (d *BaseDatabase) SaveEvent(ctx *sql.Context, ed sql.EventDefinition) error {
	loweredName := strings.ToLower(ed.Name)
	for _, existingEd := range d.events {
		if strings.ToLower(existingEd.Name) == loweredName {
			return sql.ErrEventAlreadyExists.New(ed.Name)
		}
	}
	d.events = append(d.events, ed)
	return nil
}

// DropEvent implements sql.EventDatabase
func (d *BaseDatabase) DropEvent(ctx *sql.Context, name string) error {
	loweredName := strings.ToLower(name)
	found := false
	for i, ed := range d.events {
		if strings.ToLower(ed.Name) == loweredName {
			d.events = append(d.events[:i], d.events[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		return sql.ErrEventDoesNotExist.New(name)
	}
	return nil
}

// UpdateEvent implements sql.EventDatabase
func (d *BaseDatabase) UpdateEvent(ctx *sql.Context, originalName string, ed sql.EventDefinition) error {
	loweredOriginalName := strings.ToLower(originalName)
	loweredNewName := strings.ToLower(ed.Name)
	found := false
	for i, existingEd := range d.events {
		if loweredOriginalName != loweredNewName && strings.ToLower(existingEd.Name) == loweredNewName {
			// renaming event to existing name
			return sql.ErrEventAlreadyExists.New(loweredNewName)
		} else if strings.ToLower(existingEd.Name) == loweredOriginalName {
			d.events[i] = ed
			found = true
		}
	}
	if !found {
		return sql.ErrEventDoesNotExist.New(ed.Name)
	}
	return nil
}

// GetCollation implements sql.CollatedDatabase.
func (d *BaseDatabase) GetCollation(ctx *sql.Context) sql.CollationID {
	return d.collation
}

// SetCollation implements sql.CollatedDatabase.
func (d *BaseDatabase) SetCollation(ctx *sql.Context, collation sql.CollationID) error {
	d.collation = collation
	return nil
}

// CreateView implements the interface sql.ViewDatabase.
func (d *Database) CreateView(ctx *sql.Context, name string, selectStatement, createViewStmt string) error {
	_, ok := d.views[name]
	if ok {
		return sql.ErrExistingView.New(name)
	}

	d.views[name] = sql.ViewDefinition{Name: name, TextDefinition: selectStatement, CreateViewStatement: createViewStmt}
	return nil
}

// DropView implements the interface sql.ViewDatabase.
func (d *Database) DropView(ctx *sql.Context, name string) error {
	_, ok := d.views[name]
	if !ok {
		return sql.ErrViewDoesNotExist.New(name)
	}

	delete(d.views, name)
	return nil
}

// AllViews implements the interface sql.ViewDatabase.
func (d *Database) AllViews(ctx *sql.Context) ([]sql.ViewDefinition, error) {
	var views []sql.ViewDefinition
	for _, def := range d.views {
		views = append(views, def)
	}
	return views, nil
}

// GetViewDefinition implements the interface sql.ViewDatabase.
func (d *Database) GetViewDefinition(ctx *sql.Context, viewName string) (sql.ViewDefinition, bool, error) {
	viewDef, ok := d.views[viewName]
	return viewDef, ok, nil
}

type ReadOnlyDatabase struct {
	*HistoryDatabase
}

var _ sql.ReadOnlyDatabase = ReadOnlyDatabase{}

func NewReadOnlyDatabase(name string) ReadOnlyDatabase {
	h := NewHistoryDatabase(name)
	return ReadOnlyDatabase{h}
}

func (d ReadOnlyDatabase) IsReadOnly() bool {
	return true
}
