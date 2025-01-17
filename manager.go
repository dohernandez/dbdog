// Package dbdog provides godog steps to handle database state.
//
// Database Configuration
//
// Databases instances should be configured with Manager.Instances.
//
//		dbm := dbdog.Manager{}
//
//		dbm.Instances = map[string]dbdog.Instance{
//			"my_db": {
//				Storage: storage,
//				Tables: map[string]interface{}{
//					"my_table":           new(repository.MyRow),
//					"my_another_table":   new(repository.MyAnotherRow),
//				},
//			},
//		}
//
// Table TableMapper Configuration
//
// Table mapper allows customizing decoding string values from godog table cells into Go row structures and back.
//
//		tableMapper := dbdog.NewTableMapper()
//
//		// Apply JSON decoding to a particular type.
//		tableMapper.Decoder.RegisterFunc(func(s string) (interface{}, error) {
//			m := repository.Meta{}
//			err := json.Unmarshal([]byte(s), &m)
//			if err != nil {
//				return nil, err
//			}
//			return m, err
//		}, repository.Meta{})
//
//		// Apply string splitting to github.com/lib/pq.StringArray.
//		tableMapper.Decoder.RegisterFunc(func(s string) (interface{}, error) {
//			return pq.StringArray(strings.Split(s, ",")), nil
//		}, pq.StringArray{})
//
//		// Create database manager with custom mapper.
//		dbm := dbdog.Manager{
//			TableMapper: tableMapper,
//		}
//
//
// Step Definitions
//
// Delete all rows from table.
//
//   	Given there are no rows in table "my_table" of database "my_db"
//
// Populate rows in a database with a gherkin table.
//
//	   And these rows are stored in table "my_table" of database "my_db"
//		 | id | foo   | bar | created_at           | deleted_at           |
//		 | 1  | foo-1 | abc | 2021-01-01T00:00:00Z | NULL                 |
//		 | 2  | foo-1 | def | 2021-01-02T00:00:00Z | 2021-01-03T00:00:00Z |
//		 | 3  | foo-2 | hij | 2021-01-03T00:00:00Z | 2021-01-03T00:00:00Z |
//
//  Or with an CSV file
//
//	   And rows from this file are stored in table "my_table" of database "my_db"
//		 """
//		 path/to/rows.csv
//		 """
//
// Assert rows existence in a database.
//
// For each row in gherkin table DB is queried to find a row with WHERE condition that includes
// provided column values.
//
// If a column has NULL value, it is excluded from WHERE condition.
//
// Column can contain variable (any unique string starting with $ or other prefix configured with Manager.VarPrefix).
// If variable has not yet been populated, it is excluded from WHERE condition and populated with value received
// from database. When this variable is used in next steps, it replaces the value of column with value of variable.
//
// Variables can help to assert consistency of dynamic data, for example variable can be populated as ID of one entity
// and then checked as foreign key value of another entity. This can be especially helpful in cases of UUIDs.
//
// If column value represents JSON array or object it is excluded from WHERE condition, value assertion is done
// by comparing Go value mapped from database row field with Go value mapped from gherkin table cell.
//
//	   Then these rows are available in table "my_table" of database "my_db"
//		 | id   | foo   | bar | created_at           | deleted_at           |
//		 | $id1 | foo-1 | abc | 2021-01-01T00:00:00Z | NULL                 |
//		 | $id2 | foo-1 | def | 2021-01-02T00:00:00Z | 2021-01-03T00:00:00Z |
//		 | $id3 | foo-2 | hij | 2021-01-03T00:00:00Z | 2021-01-03T00:00:00Z |
//
// Rows can be also loaded from CSV file.
//
//	   Then rows from this file are available in table "my_table" of database "my_db"
//		 """
//		 path/to/rows.csv
//		 """
//
// It is possible to check table contents exhaustively by adding "only" to step statement. Such assertion will also
// make sure that total number of rows in database table matches number of rows in gherkin table.
//
//	   Then only these rows are available in table "my_table" of database "my_db"
//		 | id   | foo   | bar | created_at           | deleted_at           |
//		 | $id1 | foo-1 | abc | 2021-01-01T00:00:00Z | NULL                 |
//		 | $id2 | foo-1 | def | 2021-01-02T00:00:00Z | 2021-01-03T00:00:00Z |
//		 | $id3 | foo-2 | hij | 2021-01-03T00:00:00Z | 2021-01-03T00:00:00Z |
//
// Rows can be also loaded from CSV file.
//
//	   Then only rows from this file are available in table "my_table" of database "my_db"
//		 """
//		 path/to/rows.csv
//		 """
//
// Assert no rows exist in a database.
//
//	   And no rows are available in table "my_another_table" of database "my_db"
package dbdog

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/bool64/shared"
	"github.com/bool64/sqluct"
	"github.com/cucumber/godog"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/swaggest/form/v5"
)

// DefaultDatabase is the name of default database.
const DefaultDatabase = "default"

// RegisterSteps adds database manager context to test suite.
func (m *Manager) RegisterSteps(s *godog.ScenarioContext) {
	m.registerPrerequisites(s)
	m.registerAssertions(s)
	s.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		if m.Vars == nil {
			m.Vars = &shared.Vars{}
		}

		m.Vars.Reset()

		return ctx, nil
	})
}

func (m *Manager) registerPrerequisites(s *godog.ScenarioContext) {
	s.Step(`no rows in table "([^"]*)" of database "([^"]*)"$`,
		m.noRowsInTableOfDatabase)

	s.Step(`no rows in table "([^"]*)"$`,
		func(tableName string) error {
			return m.noRowsInTableOfDatabase(tableName, DefaultDatabase)
		})

	s.Step(`these rows are stored in table "([^"]*)" of database "([^"]*)"[:]?$`,
		func(tableName, database string, data *godog.Table) error {
			return m.theseRowsAreStoredInTableOfDatabase(tableName, database, Rows(data))
		})

	s.Step(`rows from this file are stored in table "([^"]*)" of database "([^"]*)"[:]?$`,
		func(tableName, database string, filePath *godog.DocString) error {
			return m.rowsFromThisFileAreStoredInTableOfDatabase(tableName, database, filePath.Content)
		})

	s.Step(`these rows are stored in table "([^"]*)"[:]?$`,
		func(tableName string, data *godog.Table) error {
			return m.theseRowsAreStoredInTableOfDatabase(tableName, DefaultDatabase, Rows(data))
		})

	s.Step(`rows from this file are stored in table "([^"]*)"[:]?$`,
		func(tableName string, filePath *godog.DocString) error {
			return m.rowsFromThisFileAreStoredInTableOfDatabase(tableName, DefaultDatabase, filePath.Content)
		})
}

func (m *Manager) registerAssertions(s *godog.ScenarioContext) {
	s.Step(`only rows from this file are available in table "([^"]*)" of database "([^"]*)"[:]?$`,
		func(tableName, database string, filePath *godog.DocString) error {
			return m.onlyRowsFromThisFileAreAvailableInTableOfDatabase(tableName, database, filePath.Content)
		})

	s.Step(`only these rows are available in table "([^"]*)" of database "([^"]*)"[:]?$`,
		func(tableName, database string, data *godog.Table) error {
			return m.onlyTheseRowsAreAvailableInTableOfDatabase(tableName, database, Rows(data))
		})

	s.Step(`only rows from this file are available in table "([^"]*)"[:]?$`,
		func(tableName string, filePath *godog.DocString) error {
			return m.onlyRowsFromThisFileAreAvailableInTableOfDatabase(tableName, DefaultDatabase, filePath.Content)
		})

	s.Step(`only these rows are available in table "([^"]*)"[:]?$`,
		func(tableName string, data *godog.Table) error {
			return m.onlyTheseRowsAreAvailableInTableOfDatabase(tableName, DefaultDatabase, Rows(data))
		})

	s.Step(`no rows are available in table "([^"]*)" of database "([^"]*)"$`,
		m.noRowsAreAvailableInTableOfDatabase)

	s.Step(`no rows are available in table "([^"]*)"$`,
		func(tableName string) error {
			return m.noRowsAreAvailableInTableOfDatabase(tableName, DefaultDatabase)
		})

	s.Step(`rows from this file are available in table "([^"]*)" of database "([^"]*)"[:]?$`,
		m.rowsFromThisFileAreAvailableInTableOfDatabase)

	s.Step(`these rows are available in table "([^"]*)" of database "([^"]*)"[:]?$`,
		m.theseRowsAreAvailableInTableOfDatabase)

	s.Step(`rows from this file are available in table "([^"]*)"[:]?$`,
		func(tableName string, filePath *godog.DocString) error {
			return m.rowsFromThisFileAreAvailableInTableOfDatabase(tableName, DefaultDatabase, filePath.Content)
		})

	s.Step(`these rows are available in table "([^"]*)"[:]?$`,
		func(tableName string, data *godog.Table) error {
			return m.theseRowsAreAvailableInTableOfDatabase(tableName, DefaultDatabase, Rows(data))
		})
}

// NewManager initializes instance of database Manager.
func NewManager() *Manager {
	return &Manager{
		TableMapper: NewTableMapper(),
		Instances:   make(map[string]Instance),
	}
}

// Manager owns database connections.
type Manager struct {
	TableMapper *TableMapper
	Instances   map[string]Instance

	// Vars allow sharing vars with other steps.
	Vars *shared.Vars
}

// Instance provides database instance.
type Instance struct {
	Storage *sqluct.Storage
	// Tables is a map of row structures per table name.
	// Example: `"my_table": new(MyEntityRow)`
	Tables map[string]interface{}
	// PostNoRowsStatements is a map of SQL statement list per table name.
	// They are executed after `no rows in table` step.
	// Example: `"my_table": []string{"ALTER SEQUENCE my_table_id_seq RESTART"}`.
	PostCleanup map[string][]string
}

// RegisterJSONTypes registers types of provided values to unmarshal as JSON when decoding from string.
//
// Arguments should match types of fields in row entities.
// If field is a pointer, argument should be a pointer: e.g. new(MyType).
// If field is not a pointer, argument should not be a pointer: e.g. MyType{}.
func (m *Manager) RegisterJSONTypes(values ...interface{}) {
	for _, t := range values {
		rt := reflect.TypeOf(t)
		m.TableMapper.Decoder.RegisterFunc(func(s string) (interface{}, error) {
			v := reflect.New(rt)
			err := json.Unmarshal([]byte(s), v.Interface())

			return reflect.Indirect(v).Interface(), err
		}, t)
	}
}

func (m *Manager) noRowsInTableOfDatabase(tableName, dbName string) error {
	instance, ok := m.Instances[dbName]
	if !ok {
		return fmt.Errorf("%w %s", errUnknownDatabase, dbName)
	}

	_, ok = instance.Tables[tableName]
	if !ok {
		return fmt.Errorf("%w %s in database %s", errUnknownTable, tableName, dbName)
	}

	// Deleting from table
	_, err := instance.Storage.Exec(
		context.Background(),
		instance.Storage.DeleteStmt(tableName),
	)
	if err != nil {
		return fmt.Errorf("failed to delete from table %s in db %s: %w", tableName, dbName, err)
	}

	if instance.PostCleanup != nil {
		for _, statement := range instance.PostCleanup[tableName] {
			_, err := instance.Storage.Exec(
				context.Background(),
				sqluct.StringStatement(statement),
			)
			if err != nil {
				return fmt.Errorf("failed to execute post cleanup statement %q for table %s in db %s: %w",
					statement, tableName, dbName, err)
			}
		}
	}

	return err
}

var errMissingFileName = errors.New("missing file name")

func loadTableFromFile(filePath string) (rows [][]string, err error) {
	if filePath == "" {
		return nil, errMissingFileName
	}

	f, err := os.Open(filePath) // nolint:gosec // Intended file inclusion.
	if err != nil {
		return nil, err
	}

	defer func() { // nolint:gosec // False positive: G307: Deferring unsafe method "Close" on type "*os.File" (gosec)
		clErr := f.Close()
		if clErr != nil && err == nil {
			err = clErr
		}
	}()

	c := csv.NewReader(f)

	rows, err = c.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("failed to read CSV: %w", err)
	}

	return rows, nil
}

// Rows converts godog table to a nested slice of strings.
func Rows(data *godog.Table) [][]string {
	d := make([][]string, 0, len(data.Rows))

	for _, r := range data.Rows {
		row := make([]string, 0, len(r.Cells))

		for _, c := range r.Cells {
			row = append(row, c.Value)
		}

		d = append(d, row)
	}

	return d
}

func (m *Manager) rowsFromThisFileAreStoredInTableOfDatabase(tableName, dbName string, filePath string) error {
	data, err := loadTableFromFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to load rows from file: %w", err)
	}

	return m.theseRowsAreStoredInTableOfDatabase(tableName, dbName, data)
}

func (m *Manager) theseRowsAreStoredInTableOfDatabase(tableName, dbName string, data [][]string) error {
	instance, ok := m.Instances[dbName]
	if !ok {
		return fmt.Errorf("%w %s", errUnknownDatabase, dbName)
	}

	row, ok := instance.Tables[tableName]
	if !ok {
		return fmt.Errorf("%w %s in database %s", errUnknownTable, tableName, dbName)
	}

	m.checkInit()

	// Reading rows.
	rows, err := m.TableMapper.SliceFromTable(data, row)
	if err != nil {
		return fmt.Errorf("failed to map rows table: %w", err)
	}

	colNames := data[0]

	storage := instance.Storage
	stmt := storage.InsertStmt(tableName, rows, sqluct.Columns(colNames...))

	// Inserting rows.
	_, err = storage.Exec(context.Background(), stmt)

	if err != nil {
		query, args, toSQLErr := stmt.ToSql()
		if toSQLErr != nil {
			return toSQLErr
		}

		return fmt.Errorf("failed to insert rows %q, %v: %w", query, args, err)
	}

	return err
}

func (m *Manager) onlyRowsFromThisFileAreAvailableInTableOfDatabase(tableName, dbName string, filePath string) error {
	data, err := loadTableFromFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to load rows from file: %w", err)
	}

	return m.assertRows(tableName, dbName, data, true)
}

func (m *Manager) onlyTheseRowsAreAvailableInTableOfDatabase(tableName, dbName string, data [][]string) error {
	return m.assertRows(tableName, dbName, data, true)
}

func (m *Manager) noRowsAreAvailableInTableOfDatabase(tableName, dbName string) error {
	return m.assertRows(tableName, dbName, nil, true)
}

func (m *Manager) rowsFromThisFileAreAvailableInTableOfDatabase(tableName, dbName string, filePath string) error {
	data, err := loadTableFromFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to load rows from file: %w", err)
	}

	return m.assertRows(tableName, dbName, data, false)
}

func (m *Manager) theseRowsAreAvailableInTableOfDatabase(tableName, dbName string, data [][]string) error {
	return m.assertRows(tableName, dbName, data, false)
}

type testingT struct {
	Err error
}

func (t *testingT) Errorf(format string, args ...interface{}) {
	t.Err = fmt.Errorf(format, args...) // nolint:goerr113
}

type tableQuery struct {
	storage       *sqluct.Storage
	mapper        *TableMapper
	table         string
	data          [][]string
	row           interface{}
	colNames      []string
	skipWhereCols []string
	postCheck     []string
	vars          *shared.Vars
}

func (t *tableQuery) exposeContents(err error) error {
	qb := t.storage.SelectStmt(t.table, t.row).Limit(50)

	var colNames []string

	if t.data != nil {
		colNames = t.data[0]
	}

	table, queryErr := t.queryExistingRows(t.storage, colNames, qb)
	if queryErr != nil {
		err = fmt.Errorf("%w, failed to query existing rows: %v", err, queryErr)
	} else {
		err = fmt.Errorf("%w, rows available:\n%v", err, table)
	}

	return err
}

func (t *tableQuery) checkCount() error {
	dataCnt := 0

	if t.data != nil {
		dataCnt = len(t.data) - 1
	}

	qb := t.storage.QueryBuilder().
		Select("COUNT(1) AS c").
		From(t.table)

	cnt := struct {
		Count int `db:"c"`
	}{}

	err := t.storage.Select(context.Background(), qb, &cnt)
	if err != nil {
		return err
	}

	if cnt.Count != dataCnt {
		return fmt.Errorf("%w: %d expected, %d found",
			errInvalidNumberOfRows, dataCnt, cnt.Count)
	}

	return nil
}

func (m *Manager) makeTableQuery(tableName, dbName string, data [][]string) (*tableQuery, error) {
	instance, ok := m.Instances[dbName]
	if !ok {
		return nil, fmt.Errorf("%w %s", errUnknownDatabase, dbName)
	}

	row, ok := instance.Tables[tableName]
	if !ok {
		return nil, fmt.Errorf("%w %s in database %s", errUnknownTable, tableName, dbName)
	}

	m.checkInit()

	t := tableQuery{
		storage: instance.Storage,
		mapper:  m.TableMapper,
		table:   tableName,
		data:    data,
		row:     row,
		vars:    m.Vars,
	}

	if t.data != nil {
		t.colNames = data[0]
		t.skipWhereCols = make([]string, 0, len(t.colNames))
		t.postCheck = make([]string, 0, len(t.colNames))
	}

	return &t, nil
}

func (t *tableQuery) receiveRow(index int, row interface{}, _ []string, rawValues []string) error {
	qb := t.storage.QueryBuilder().
		Select(t.colNames...).
		From(t.table)

	eq := t.storage.WhereEq(row, sqluct.Columns(t.colNames...))

	for _, sk := range t.skipWhereCols {
		delete(eq, sk)
	}

	t.skipWhereCols = t.skipWhereCols[:0]

	for _, col := range t.colNames {
		if _, ok := eq[col]; !ok {
			continue
		}

		qb = qb.Where(squirrel.Eq{col: eq[col]})
	}

	dest := reflect.New(reflect.TypeOf(row).Elem()).Interface()

	err := t.storage.Select(context.Background(), qb, dest)
	if err != nil {
		query, args, qbErr := qb.ToSql()
		if qbErr != nil {
			return fmt.Errorf("failed to build query: %w", qbErr)
		}

		return fmt.Errorf("failed to query row %d (%+v) with %q %v: %w", index, row, query, args, err)
	}

	colOption := sqluct.Columns(t.colNames...)

	pc := t.postCheck
	t.postCheck = t.postCheck[:0]

	return t.doPostCheck(t.colNames, pc,
		combine(t.storage.Mapper.ColumnsValues(reflect.ValueOf(row), colOption)),
		combine(t.storage.Mapper.ColumnsValues(reflect.ValueOf(dest), colOption)),
		rawValues)
}

func combine(keys []string, vals []interface{}) map[string]interface{} {
	m := make(map[string]interface{}, len(keys))
	for i, k := range keys {
		m[k] = vals[i]
	}

	return m
}

func (t *tableQuery) skipDecode(column, value string) bool {
	// Databases do not provide JSON equality conditions in general,
	// so if value looks like a non-scalar JSON it is removed from WHERE condition and checked for equality
	// using Go values during post processing.
	if len(value) > 0 && (value[0] == '{' || value[0] == '[') && json.Valid([]byte(value)) {
		t.postCheck = append(t.postCheck, column)
		t.skipWhereCols = append(t.skipWhereCols, column)

		return false
	}

	// If value looks like a variable name and does not have an associated value yet,
	// it is removed from decoding and WHERE condition.
	if t.vars.IsVar(value) {
		if _, found := t.vars.Get(value); found {
			return false
		}

		t.skipWhereCols = append(t.skipWhereCols, column)

		return true
	}

	return false
}

func (t *tableQuery) makeReplaces(onSetErr *error) (map[string]string, error) {
	replaces := make(map[string]string)

	if vars := t.vars.GetAll(); len(vars) > 0 {
		replaces = make(map[string]string, len(vars))

		for k, v := range vars {
			s, err := t.mapper.Encode(v)
			if err != nil {
				return nil, err
			}

			replaces[k] = s
		}
	}

	t.vars.OnSet(func(key string, val interface{}) {
		s, err := t.mapper.Encode(val)
		if err != nil {
			*onSetErr = err
		}

		replaces[key] = s
	})

	return replaces, nil
}

func (m *Manager) assertRows(tableName, dbName string, data [][]string, exhaustiveList bool) (err error) {
	t, err := m.makeTableQuery(tableName, dbName, data)
	if err != nil {
		return err
	}

	defer func() {
		// Expose table contents to simplify test debugging.
		if err != nil {
			err = t.exposeContents(err)
		}
	}()

	if exhaustiveList {
		err = t.checkCount()
		if err != nil {
			return err
		}
	}

	if data == nil {
		return nil
	}

	var onSetErr error

	replaces, err := t.makeReplaces(&onSetErr)
	if err != nil {
		return err
	}

	// Iterating rows.
	err = m.TableMapper.IterateTable(IterateConfig{
		Data:       data,
		Item:       t.row,
		SkipDecode: t.skipDecode,
		Replaces:   replaces,
		ReceiveRow: t.receiveRow,
	})

	if err == nil && onSetErr != nil {
		err = onSetErr
	}

	return err
}

func (t *tableQuery) doPostCheck(colNames []string, postCheck []string, argsExp, argsRcv map[string]interface{}, rawValues []string) error {
	for i, name := range colNames {
		if t.vars.IsVar(rawValues[i]) {
			t.vars.Set(rawValues[i], argsRcv[name])
		}

		pc := false

		for _, col := range postCheck {
			if col == name {
				pc = true

				break
			}
		}

		if !pc {
			continue
		}

		te := testingT{}

		assert.Equal(&te, indirect(argsExp[name]), indirect(argsRcv[name]))

		if te.Err != nil {
			return fmt.Errorf("unexpected row contents at column %s (%#v, %#v): %w",
				name, indirect(argsExp[name]), indirect(argsRcv[name]), te.Err)
		}
	}

	return nil
}

func indirect(v interface{}) interface{} {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}

	return rv.Interface()
}

func (m *Manager) checkInit() {
	if m.TableMapper == nil {
		m.TableMapper = NewTableMapper()
	}
}

// ParseTime tries to parse time in multiple formats.
func ParseTime(s string, formats ...string) (time.Time, error) {
	if len(formats) == 0 {
		formats = []string{
			time.RFC3339Nano,
			"2006-01-02T15:04:05.999999999",
			"2006-01-02 15:04:05",
			"2006-01-02",
			time.RFC3339,
		}
	}

	var (
		t   time.Time
		err error
	)

	for _, f := range formats {
		if t, err = time.Parse(f, s); err == nil {
			return t, nil
		}
	}

	return t, err
}

// NewTableMapper creates tablestruct.TableMapper with db field decoder.
func NewTableMapper() *TableMapper {
	tm := &TableMapper{
		Decoder: form.NewDecoder(),
		Encoder: form.NewEncoder(),
	}
	tm.Decoder.RegisterFunc(func(s string) (interface{}, error) {
		return ParseTime(s)
	}, time.Time{})
	tm.Decoder.RegisterFunc(func(s string) (interface{}, error) {
		t, err := ParseTime(s)
		if err != nil {
			return nil, err
		}

		return &t, nil
	}, new(time.Time))

	tm.Decoder.SetMode(form.ModeExplicit)
	tm.Decoder.SetTagName("db")
	form.RegisterSQLNullTypesDecodeFunc(tm.Decoder)

	tm.Encoder.SetMode(form.ModeExplicit)
	tm.Encoder.SetTagName("db")
	form.RegisterSQLNullTypesEncodeFunc(tm.Encoder, null)

	return tm
}

var (
	errWrongType           = errors.New("failed to assert type *interface{}")
	errInvalidNumberOfRows = errors.New("invalid number of rows in table")
	errUnknownTable        = errors.New("unknown table")
	errUnknownDatabase     = errors.New("unknown database")
)

func (t *tableQuery) queryExistingRows(db *sqluct.Storage, colNames []string, qb squirrel.Sqlizer) (table string, err error) {
	rows, err := db.Query(context.Background(), qb)
	if err != nil {
		return "", err
	}

	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}

	if len(colNames) == 0 {
		colNames = cols
	}

	var (
		width = map[string]int{}
		res   = make(map[string][]string)
	)

	for _, col := range colNames {
		width[col] = len(col)
	}

	cnt := 0

	for rows.Next() {
		cnt++

		err = t.formatRow(rows, cols, width, res)
		if err != nil {
			return "", err
		}
	}

	result := t.renderRows(colNames, res, width, cnt)

	return result, rows.Err()
}

func (t *tableQuery) renderRows(colNames []string, res map[string][]string, width map[string]int, cnt int) string {
	result := "|"

	for _, col := range colNames {
		if _, ok := res[col]; !ok {
			continue
		}

		result += " " + col + strings.Repeat(" ", width[col]-len(col)) + " |"
	}

	result += "\n"

	for i := 0; i < cnt; i++ {
		result += "|"

		for _, col := range colNames {
			vv, ok := res[col]
			if !ok {
				continue
			}

			v := vv[i]
			result += " " + v + strings.Repeat(" ", width[col]-len(v)) + " |"
		}

		result += "\n"
	}

	return result
}

func (t *tableQuery) formatRow(rows *sqlx.Rows, cols []string, width map[string]int, res map[string][]string) error {
	// Create a slice of interface{} to represent each column,
	// and a second slice to contain pointers to each item in the columns slice.
	columns := make([]interface{}, len(cols))
	columnPointers := make([]interface{}, len(cols))

	for i := range columns {
		columnPointers[i] = &columns[i]
	}

	// Scan the result into the column pointers.
	if err := rows.Scan(columnPointers...); err != nil {
		return err
	}

	// Create map and retrieve the value for each column from the pointers slice,
	// storing it in the map with the name of the column as the key.
	for i, col := range cols {
		val, ok := columnPointers[i].(*interface{})
		if !ok {
			return fmt.Errorf("%w of %T", errWrongType, columnPointers[i])
		}

		var v string

		if *val == nil {
			v = null
		} else if b, ok := (*val).([]byte); ok {
			v = string(b)
		} else {
			s, err := t.mapper.Encode(*val)
			if err != nil {
				return err
			}

			v = s
		}

		if len(v) > width[col] {
			width[col] = len(v)
		}

		res[col] = append(res[col], v)
	}

	return nil
}
